// Package service provides the local control-plane foundation. It does not run inference.
package service

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

type Config struct {
	Listen          string
	Backend         string
	BackendGRPC     string
	RequestTimeout  time.Duration
	MaxOutputTokens int    // Public per-request cap; must match the native runtime configuration.
	AuthToken       string // Optional local bearer secret; never log Config or this field.
	RatePerMinute   int    // Zero disables the shared generation request limiter.
	RateBurst       int    // Zero selects one request when rate limiting is enabled.
	StudioDir       string // Optional absolute production bundle directory.
	ModelConfigDir  string // Optional private directory used by the next-start model selector.
	ManagedBackend  ManagedBackend
}

// Only explicit loopback IPs are accepted; no DNS or wildcard binding.
func loopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("expected loopback IP:port: %w", err)
	}
	ip := net.ParseIP(host)
	n, err := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || err != nil || n < 1 || n > 65535 {
		return errors.New("address must use a loopback IP and port 1..65535")
	}
	return nil
}

func (c Config) Validate() error {
	if c.StudioDir != "" {
		if !filepath.IsAbs(c.StudioDir) {
			return errors.New("studio directory must be absolute")
		}
		if err := validateStudioDir(c.StudioDir); err != nil {
			return fmt.Errorf("studio directory: %w", err)
		}
	}
	if c.ModelConfigDir != "" {
		if !filepath.IsAbs(c.ModelConfigDir) {
			return errors.New("model config directory must be absolute")
		}
		info, err := os.Lstat(c.ModelConfigDir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("model config directory must be an existing non-symlink directory")
		}
	}
	if c.RatePerMinute < 0 || c.RatePerMinute > 60000 || c.RateBurst < 0 || c.RateBurst > 1024 || (c.RatePerMinute == 0 && c.RateBurst != 0) {
		return errors.New("rate must be 0..60000 requests/minute; burst 0..1024 requires an enabled rate (zero burst selects one)")
	}
	if c.AuthToken != "" && !validAuthToken(c.AuthToken) {
		return errors.New("auth token must be 32..256 bearer-token characters without whitespace")
	}
	if c.RequestTimeout < 0 || c.RequestTimeout > time.Hour {
		return errors.New("request timeout must be positive and at most one hour (zero selects default)")
	}
	if c.MaxOutputTokens < 0 || c.MaxOutputTokens > 1048576 {
		return errors.New("maximum output tokens must be 1..1048576 (zero selects 512)")
	}
	if err := loopbackAddress(c.Listen); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if c.ManagedBackend != nil {
		if c.Backend != "" || c.BackendGRPC != "" {
			return errors.New("managed backend cannot be combined with external targets")
		}
		return nil
	}
	if c.BackendGRPC != "" {
		if c.Backend != "" {
			return errors.New("choose HTTP compatibility probe or gRPC backend, not both")
		}
		return rpcbackend.ValidateTarget(c.BackendGRPC)
	}
	u, err := url.Parse(c.Backend)
	if err != nil {
		return fmt.Errorf("backend URL: %w", err)
	}
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("backend must be an http loopback origin without credentials, query, fragment, or path")
	}
	if err := loopbackAddress(u.Host); err != nil {
		return fmt.Errorf("backend: %w", err)
	}
	return nil
}

type readiness struct {
	APIVersion string `json:"api_version"`
	Ready      bool   `json:"ready"`
}

type Service struct {
	backend            string
	client             *http.Client
	rpc                inferenceClient
	ids                atomic.Uint64
	active             chan struct{}
	requestTimeout     time.Duration
	maxOutputTokens    int
	authDigest         *[32]byte
	limiter            *requestLimiter
	admissionMu        sync.Mutex
	draining           bool
	generationHandlers int
	lifecycleMu        sync.Mutex
	managed            ManagedBackend
	history            requestHistory
	observedSince      time.Time
	studio             fs.FS
	modelConfigDir     string
}

type inferenceClient interface {
	Status(context.Context) (rpcbackend.StatusObservation, error)
	Generate(context.Context, *pb.GenerateRequest, func([]byte) error) (rpcbackend.Observation, error)
	GeneratePieces(context.Context, *pb.GenerateRequest, func(rpcbackend.OutputPiece) error) (rpcbackend.Observation, error)
	Close() error
}

type ManagedBackend interface {
	inferenceClient
	State() string
	Start(context.Context) error
	Stop(context.Context) error
}

func New(c Config) (*Service, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	s := &Service{active: make(chan struct{}, 4), requestTimeout: c.RequestTimeout,
		maxOutputTokens: c.MaxOutputTokens, observedSince: time.Now()}
	if s.maxOutputTokens == 0 {
		s.maxOutputTokens = 512
	}
	s.modelConfigDir = c.ModelConfigDir
	if c.StudioDir != "" {
		s.studio = os.DirFS(c.StudioDir)
	}
	if c.RatePerMinute > 0 {
		burst := c.RateBurst
		if burst == 0 {
			burst = 1
		}
		s.limiter = newRequestLimiter(c.RatePerMinute, burst, time.Now())
	}
	if c.AuthToken != "" {
		s.authDigest = tokenDigest(c.AuthToken)
	}
	if s.requestTimeout == 0 {
		s.requestTimeout = time.Minute
	}
	var seed [8]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}
	s.ids.Store(binary.LittleEndian.Uint64(seed[:]) & ((uint64(1) << 63) - 1))
	if c.ManagedBackend != nil {
		s.rpc, s.managed = c.ManagedBackend, c.ManagedBackend
		return s, nil
	}
	if c.BackendGRPC != "" {
		client, err := rpcbackend.New(c.BackendGRPC)
		if err != nil {
			return nil, err
		}
		s.rpc = client
		return s, nil
	}
	// Ignore system proxy settings; never follow redirects away from the origin.
	transport := &http.Transport{
		DialContext:     (&net.Dialer{Timeout: time.Second}).DialContext,
		MaxConnsPerHost: 8, MaxIdleConnsPerHost: 2,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: time.Second,
	}
	s.backend = c.Backend
	s.client = &http.Client{
		Transport: transport, Timeout: time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return s, nil
}

func (s *Service) Close() {
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
	if s.rpc != nil {
		_ = s.rpc.Close()
	}
}

func (s *Service) backendReady(ctx context.Context) bool {
	if s.rpc != nil {
		result, err := s.rpc.Status(ctx)
		return err == nil && result.Response.State == pb.BackendState_BACKEND_STATE_READY
	}
	u, _ := url.Parse(s.backend) // validated at construction
	u.Path = "/internal/v1/ready"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	// Bound the full response and reject trailing garbage or incompatible versions.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(body) > 4096 {
		return false
	}
	var state readiness
	if json.Unmarshal(body, &state) != nil {
		return false
	}
	return state.APIVersion == "1" && state.Ready
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	_ = setWriteDeadline(w)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	if s.rpc != nil {
		mux.Handle("GET /v1/models", s.authenticate(http.HandlerFunc(s.models)))
		mux.Handle("GET /v1/models/{model}", s.authenticate(http.HandlerFunc(s.models)))
		mux.Handle("POST /v1/chat/completions", s.authenticate(s.generationAdmission(s.rateLimit(http.HandlerFunc(s.chat)))))
		mux.Handle("GET /internal/v1/admission", s.control(http.HandlerFunc(s.admissionStatus)))
		mux.Handle("POST /internal/v1/admission/drain", s.control(http.HandlerFunc(s.drain)))
		mux.Handle("POST /internal/v1/admission/resume", s.control(http.HandlerFunc(s.resume)))
		mux.Handle("GET /internal/v1/backend", s.control(http.HandlerFunc(s.backendStatus)))
		mux.Handle("POST /internal/v1/backend/start", s.control(http.HandlerFunc(s.backendStart)))
		mux.Handle("POST /internal/v1/backend/stop", s.control(http.HandlerFunc(s.backendStop)))
		mux.Handle("GET /internal/v1/requests", s.control(http.HandlerFunc(s.requestHistoryHandler)))
		mux.Handle("GET /internal/v1/requests/{request}", s.control(http.HandlerFunc(s.requestHistoryHandler)))
		mux.Handle("GET /internal/v1/metrics", s.control(http.HandlerFunc(s.aggregateMetrics)))
		mux.Handle("GET /internal/v1/scheduler", s.control(http.HandlerFunc(s.schedulerStatus)))
		mux.Handle("GET /internal/v1/memory", s.control(http.HandlerFunc(s.memoryStatus)))
		mux.Handle("GET /internal/v1/model-capabilities", s.control(http.HandlerFunc(s.modelCapabilities)))
		if s.modelConfigDir != "" {
			mux.Handle("GET /internal/v1/model-config", s.control(http.HandlerFunc(s.modelConfig)))
			mux.Handle("PUT /internal/v1/model-config", s.control(http.HandlerFunc(s.modelConfig)))
			mux.Handle("POST /internal/v1/model-config/apply", s.control(http.HandlerFunc(s.applyModelConfig)))
		}
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "inferno-controlplane"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.admissionSnapshot().State != "accepting" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false, "reason": "admission_draining"})
			return
		}
		if !s.backendReady(r.Context()) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false, "reason": "backend_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ready": true})
	})
	if s.studio != nil {
		mux.Handle("GET /", s.studioHandler())
	}
	return mux
}

func validateStudioDir(root string) error {
	info, err := os.Stat(filepath.Join(root, "index.html"))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("index.html is missing or not a regular file")
	}
	return filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed: %s", name)
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular asset is not allowed: %s", name)
		}
		return nil
	})
}

func (s *Service) studioHandler() http.Handler {
	files := http.FileServerFS(s.studio)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never turn misspelled service endpoints into successful HTML responses.
		for _, prefix := range []string{"/v1", "/internal", "/healthz", "/readyz"} {
			if r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/") {
				http.NotFound(w, r)
				return
			}
		}
		asset := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if asset == "." {
			asset = "index.html"
		}
		if info, err := fs.Stat(s.studio, asset); err != nil || !info.Mode().IsRegular() {
			asset = "index.html"
		}
		if asset == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
			body, err := fs.ReadFile(s.studio, asset)
			if err != nil {
				http.Error(w, "Studio unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			if r.Method != http.MethodHead {
				_, _ = w.Write(body)
			}
			return
		}
		clone := r.Clone(r.Context())
		clone.URL.Path = "/" + asset
		files.ServeHTTP(w, clone)
	})
}

// Run serves until cancellation, then waits for shutdown (at most five seconds).
// Request contexts cancel RPCs; stream writes have their own bounded deadlines.
func (s *Service) Run(ctx context.Context, listen string) error {
	if err := loopbackAddress(listen); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout: 5 * time.Second, WriteTimeout: 0, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 16 * 1024,
		BaseContext:    func(net.Listener) context.Context { return ctx },
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = server.Shutdown(shutdownCtx)
		if err != nil {
			_ = server.Close()
		}
		serveErr := <-done // always join the serving goroutine
		if err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		if !errors.Is(serveErr, http.ErrServerClosed) {
			return serveErr
		}
		return nil
	}
}
