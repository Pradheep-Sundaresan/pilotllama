package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"inferno.local/controlplane/internal/backendprocess"
	"inferno.local/controlplane/internal/service"
)

func main() {
	c := service.Config{}
	managed := backendprocess.Config{}
	var authTokenEnv string
	var contextTokens uint
	var maxOutputTokens uint
	flag.StringVar(&authTokenEnv, "auth-token-env", "", "opt-in bearer auth: name of environment variable holding a 32..256 character local token")
	flag.IntVar(&c.RatePerMinute, "rate-per-minute", 0, "shared generation requests/minute, 0 disables, maximum 60000")
	flag.IntVar(&c.RateBurst, "rate-burst", 0, "generation burst allowance, 0 selects one when rate enabled, maximum 1024")
	flag.StringVar(&managed.Executable, "backend-exec", "", "opt-in absolute local C++ backend executable (managed child)")
	flag.StringVar(&managed.Model, "model", "", "absolute existing local GGUF for managed backend; never downloaded")
	flag.StringVar(&managed.DraftModel, "draft-model", "", "optional absolute existing DFlash GGUF for queue-mode acceleration")
	flag.StringVar(&managed.ModelID, "model-id", "local-nemotron-4b", "managed backend model alias")
	flag.StringVar(&managed.Batching, "backend-batching", "queue", "managed native scheduler: queue or continuous")
	flag.BoolVar(&managed.QualifyContinuous, "backend-qualify-continuous", false, "allow continuous mode only for an externally qualified exact local model")
	flag.UintVar(&contextTokens, "context-tokens", 4096, "native context budget in tokens, bounded by the loaded model")
	flag.UintVar(&maxOutputTokens, "max-output-tokens", 512, "maximum generated tokens per request")
	flag.StringVar(&managed.StateDir, "backend-state-dir", "build/backend", "project-local managed backend state directory")
	flag.DurationVar(&managed.StartupTimeout, "backend-startup-timeout", 2*time.Minute, "managed backend readiness budget, at most 1h")
	flag.DurationVar(&managed.ShutdownTimeout, "backend-shutdown-timeout", 15*time.Second, "managed child shutdown grace before kill, at most 1m")
	flag.StringVar(&c.Listen, "listen", "127.0.0.1:8080", "loopback listen IP:port")
	flag.StringVar(&c.Backend, "backend", "http://127.0.0.1:8081", "legacy HTTP readiness probe origin")
	flag.StringVar(&c.BackendGRPC, "backend-grpc", "", "opt-in C++ gRPC loopback IP:port")
	flag.StringVar(&c.StudioDir, "studio-dir", "", "optional absolute local Studio production bundle directory")
	flag.StringVar(&c.ModelConfigDir, "model-config-dir", "", "optional absolute private next-start model configuration directory")
	flag.DurationVar(&c.RequestTimeout, "request-timeout", time.Minute, "generation request budget (positive, at most 1h)")
	flag.Parse()
	if contextTokens > 1048576 || maxOutputTokens > 1048576 {
		slog.Error("context or output token budget exceeds supported bound")
		os.Exit(2)
	}
	managed.ContextTokens, managed.MaxOutputTokens = uint32(contextTokens), uint32(maxOutputTokens)
	c.MaxOutputTokens = int(maxOutputTokens)
	if authTokenEnv != "" {
		var ok bool
		c.AuthToken, ok = os.LookupEnv(authTokenEnv)
		if !ok || c.AuthToken == "" {
			slog.Error("configured auth token environment variable is missing or empty")
			os.Exit(2)
		}
		// Do not pass the HTTP credential to a managed native child.
		if err := os.Unsetenv(authTokenEnv); err != nil {
			slog.Error("cannot remove auth credential from child environment")
			os.Exit(2)
		}
	}
	if c.BackendGRPC != "" || managed.Executable != "" {
		explicitHTTP := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "backend" {
				explicitHTTP = true
			}
		})
		if !explicitHTTP {
			c.Backend = ""
		}
	}
	if flag.NArg() != 0 {
		slog.Error("unexpected positional arguments")
		os.Exit(2)
	}
	if managed.Executable != "" {
		if c.Backend != "" || c.BackendGRPC != "" {
			slog.Error("managed backend cannot be combined with --backend or --backend-grpc")
			os.Exit(2)
		}
		if err := managed.Validate(); err != nil {
			slog.Error("invalid managed configuration", "error", err)
			os.Exit(2)
		}
		// Validate HTTP configuration before starting the child. Its real port is assigned later.
		c.BackendGRPC = "127.0.0.1:1"
	} else if managed.Model != "" {
		slog.Error("--model requires --backend-exec")
		os.Exit(2)
	}
	if err := c.Validate(); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	runService := func(ctx context.Context, target string) error {
		if target != "" {
			c.BackendGRPC = target
		}
		s, err := service.New(c)
		if err != nil {
			return err
		}
		defer s.Close()
		slog.Info("starting control plane", "listen", c.Listen, "backend", c.Backend, "backend_grpc", c.BackendGRPC, "auth_enabled", c.AuthToken != "")
		return s.Run(ctx, c.Listen)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var err error
	if managed.Executable != "" {
		var manager *backendprocess.Manager
		manager, err = backendprocess.NewManager(ctx, managed)
		if err == nil {
			err = manager.Start(ctx)
			if err == nil {
				c.BackendGRPC, c.ManagedBackend = "", manager
				err = runService(ctx, "")
			}
			if ctx.Err() != nil && err == ctx.Err() {
				err = nil
			}
			err = errors.Join(err, manager.Close())
		}
	} else {
		err = runService(ctx, "")
	}
	stop()
	if err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
	slog.Info("service stopped cleanly")
}
