package backendprocess

import (
	"context"
	"os/exec"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

// Manager owns at most one joined run cycle and presents a stable RPC facade
// while the child's OS-assigned address changes across explicit starts.
type Manager struct {
	operation sync.Mutex
	mu        sync.Mutex
	parent    context.Context
	config    Config
	command   func(string, ...string) *exec.Cmd
	probe     func(context.Context, string, string) bool
	state     string
	client    *rpcbackend.Client
	cancel    context.CancelFunc
	done      chan struct{}
	cycleErr  error
	closed    bool
}

func NewManager(ctx context.Context, c Config) (*Manager, error) {
	return newManager(ctx, c, func(path string, args ...string) *exec.Cmd { return exec.Command(path, args...) }, ready)
}

func newManager(ctx context.Context, c Config, command func(string, ...string) *exec.Cmd, probe func(context.Context, string, string) bool) (*Manager, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &Manager{parent: ctx, config: c, command: command, probe: probe, state: "stopped"}, nil
}

func (m *Manager) State() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Reconfigure replaces only operator-selectable runtime values while fully stopped.
func (m *Manager) Reconfigure(model, draftModel, modelID, batching string, contextTokens, maxOutputTokens uint32, qualify bool) error {
	if !m.operation.TryLock() {
		return status.Error(codes.Aborted, "lifecycle operation in progress")
	}
	defer m.operation.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.state != "stopped" {
		return status.Error(codes.Aborted, "backend must be stopped before reconfiguration")
	}
	next := m.config
	next.Model, next.DraftModel, next.ModelID, next.Batching = model, draftModel, modelID, batching
	next.ContextTokens, next.MaxOutputTokens, next.QualifyContinuous = contextTokens, maxOutputTokens, qualify
	if err := next.Validate(); err != nil {
		return err
	}
	m.config = next
	return nil
}

func (m *Manager) Start(ctx context.Context) error {
	if !m.operation.TryLock() {
		return status.Error(codes.Aborted, "lifecycle operation in progress")
	}
	defer m.operation.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed || m.parent.Err() != nil {
		m.mu.Unlock()
		return status.Error(codes.Unavailable, "manager closed")
	}
	if m.state == "ready" {
		m.mu.Unlock()
		return nil
	}
	if m.done != nil {
		select {
		case <-m.done:
		default:
			m.mu.Unlock()
			return status.Error(codes.Aborted, "backend cycle still running")
		}
	}
	cycle, cancel := context.WithCancel(m.parent)
	done, published := make(chan struct{}), make(chan struct{}, 1)
	m.cancel, m.done, m.cycleErr, m.state = cancel, done, nil, "starting"
	m.mu.Unlock()
	go func() {
		err := run(cycle, m.config, func(ctx context.Context, target string) error {
			client, err := rpcbackend.New(target)
			if err != nil {
				return err
			}
			defer client.Close()
			m.mu.Lock()
			m.client, m.state = client, "ready"
			m.mu.Unlock()
			published <- struct{}{}
			<-ctx.Done()
			return nil
		}, m.command, m.probe)
		cancel()
		m.mu.Lock()
		m.client, m.cycleErr = nil, err
		m.state = "stopped"
		if err != nil {
			m.state = "failed"
		}
		close(done)
		m.mu.Unlock()
	}()
	select {
	case <-published:
		return nil
	case <-done:
		return status.Error(codes.Unavailable, "backend did not become ready")
	case <-ctx.Done():
		cancel()
		<-done // Do not orphan a child on an HTTP disconnect/start timeout.
		return ctx.Err()
	}
}

// Once stop begins, cleanup is joined even if the caller disconnects. The child
// runner enforces its shutdown grace and reports forced kill as failure.
func (m *Manager) Stop(context.Context) error {
	if !m.operation.TryLock() {
		return status.Error(codes.Aborted, "lifecycle operation in progress")
	}
	defer m.operation.Unlock()
	return m.stop()
}

func (m *Manager) stop() error {
	m.mu.Lock()
	done, cancel := m.done, m.cancel
	if done == nil {
		m.mu.Unlock()
		return nil
	}
	select {
	case <-done:
	default:
		m.state = "stopping"
	}
	m.mu.Unlock()
	cancel()
	<-done
	m.mu.Lock()
	err := m.cycleErr
	m.mu.Unlock()
	if err != nil {
		return status.Error(codes.Internal, "backend stopped with failure; child reaped")
	}
	return nil
}

func (m *Manager) Close() error {
	m.operation.Lock()
	defer m.operation.Unlock()
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return m.stop()
}

func (m *Manager) rpc() (*rpcbackend.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != "ready" || m.client == nil {
		return nil, status.Error(codes.Unavailable, "managed backend not ready")
	}
	return m.client, nil
}

func (m *Manager) Status(ctx context.Context) (rpcbackend.StatusObservation, error) {
	client, err := m.rpc()
	if err != nil {
		return rpcbackend.StatusObservation{}, err
	}
	return client.Status(ctx)
}

func (m *Manager) Generate(ctx context.Context, request *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
	client, err := m.rpc()
	if err != nil {
		return rpcbackend.Observation{}, err
	}
	return client.Generate(ctx, request, consume)
}

func (m *Manager) GeneratePieces(ctx context.Context, request *pb.GenerateRequest, consume func(rpcbackend.OutputPiece) error) (rpcbackend.Observation, error) {
	client, err := m.rpc()
	if err != nil {
		return rpcbackend.Observation{}, err
	}
	return client.GeneratePieces(ctx, request, consume)
}
