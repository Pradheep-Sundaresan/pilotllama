package backendprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

// A real child, with no sockets or model, exercises wait/signal/reap ownership.
func TestChild(t *testing.T) {
	mode := os.Getenv("INFERNO_LIFECYCLE_TEST_CHILD")
	if mode == "" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	if mode == "exit" {
		os.Exit(3)
	}
	if mode != "loading" {
		if err := os.WriteFile(os.Getenv("INFERNO_LIFECYCLE_READY"), []byte("12345\n"), 0600); err != nil {
			os.Exit(4)
		}
	}
	if mode == "crash" {
		time.Sleep(200 * time.Millisecond)
		os.Exit(5)
	}
	for {
		<-signals
		if mode != "stuck" {
			os.Exit(0)
		}
	}
}

func config(t *testing.T) Config {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(model, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return Config{Executable: exe, Model: model, ModelID: "test", StateDir: t.TempDir(), StartupTimeout: 3 * time.Second, ShutdownTimeout: 2 * time.Second}
}

func child(mode string) func(string, ...string) *exec.Cmd {
	return func(exe string, args ...string) *exec.Cmd {
		cmd := exec.Command(exe, "-test.run=^TestChild$")
		cmd.Env = append(os.Environ(), "INFERNO_LIFECYCLE_TEST_CHILD="+mode, "INFERNO_LIFECYCLE_READY="+args[7])
		return cmd
	}
}

func TestOwnedLifecycle(t *testing.T) {
	for _, mode := range []string{"ready", "exit", "loading", "unready", "crash", "stuck", "serve-error", "cancel-loading"} {
		t.Run(mode, func(t *testing.T) {
			c := config(t)
			if mode == "loading" || mode == "unready" {
				c.StartupTimeout = 150 * time.Millisecond
			}
			if mode == "stuck" {
				c.ShutdownTimeout = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if mode == "cancel-loading" {
				ctx2, stop := context.WithTimeout(ctx, 150*time.Millisecond)
				defer stop()
				ctx = ctx2
			}
			childMode := mode
			if mode == "cancel-loading" {
				childMode = "loading"
			}
			called := false
			err := run(ctx, c, func(ctx context.Context, target string) error {
				called = true
				if target != "127.0.0.1:12345" {
					t.Errorf("unexpected target %s", target)
				}
				if mode == "serve-error" {
					return errors.New("serve failed")
				}
				if mode != "crash" {
					cancel()
				}
				<-ctx.Done()
				return nil
			}, child(childMode), func(context.Context, string, string) bool { return mode != "unready" })
			wantErr := mode != "ready" && mode != "cancel-loading"
			if (err != nil) != wantErr {
				t.Fatalf("err=%v wantErr=%v", err, wantErr)
			}
			wantCalled := mode != "exit" && mode != "loading" && mode != "unready" && mode != "cancel-loading"
			if called != wantCalled {
				t.Fatalf("serve called=%v want=%v", called, wantCalled)
			}
			if mode == "stuck" && !strings.Contains(err.Error(), "killed and reaped") {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(c.StateDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("state cleanup: %v %v", entries, err)
			}
		})
	}
}

func TestInvalidConfigNeverStarts(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Executable = "relative" },
		func(c *Config) { c.Model = c.StateDir },
		func(c *Config) { c.ModelID = "bad alias" },
		func(c *Config) { c.StartupTimeout = 0 },
		func(c *Config) { c.ShutdownTimeout = 2 * time.Minute },
		func(c *Config) { c.StateDir = "" },
		func(c *Config) { c.Batching = "unbounded" },
	} {
		c := config(t)
		mutate(&c)
		err := run(context.Background(), c, nil, func(string, ...string) *exec.Cmd { t.Fatal("started invalid config"); return nil }, nil)
		if err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
}

func TestIntegrationFixtureLifecycle(t *testing.T) {
	fixture := os.Getenv("INFERNO_RPC_FIXTURE")
	if fixture == "" {
		t.Skip("explicit model-free RPC fixture required")
	}
	c := config(t)
	c.Executable, c.ModelID = fixture, "model-free-test-fixture"
	for cycle := 0; cycle < 2; cycle++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := run(ctx, c, func(ctx context.Context, target string) error {
			client, err := rpcbackend.New(target)
			if err != nil {
				return err
			}
			defer client.Close()
			var text strings.Builder
			obs, err := client.Generate(ctx, &pb.GenerateRequest{RequestId: 1, Prompt: "tokens", MaxOutputTokens: 8}, func(b []byte) error { text.Write(b); return nil })
			if err != nil {
				return err
			}
			if text.String() != "😀!" || !obs.TransportComplete || obs.Finished.GetState() != pb.TerminalState_TERMINAL_STATE_COMPLETED {
				return errors.New("fixture completion mismatch")
			}
			cancel()
			return nil
		}, func(exe string, args ...string) *exec.Cmd { return exec.Command(exe, args[7]) }, ready)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}
