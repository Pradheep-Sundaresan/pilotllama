package backendprocess

import (
	"context"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func waitState(t *testing.T, m *Manager, state string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for m.State() != state {
		select {
		case <-timer.C:
			t.Fatalf("state=%s want=%s", m.State(), state)
		case <-ticker.C:
		}
	}
}

func TestManagerStartStopRecovery(t *testing.T) {
	c := config(t)
	var starts atomic.Int32
	m, err := newManager(context.Background(), c, func(exe string, args ...string) *exec.Cmd {
		mode := "ready"
		if starts.Add(1) == 1 {
			mode = "exit"
		}
		return child(mode)(exe, args...)
	}, func(context.Context, string, string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Start(context.Background()); err == nil || m.State() != "failed" {
		t.Fatalf("startup failure: %v %s", err, m.State())
	}
	for cycle := 0; cycle < 2; cycle++ {
		if err := m.Start(context.Background()); err != nil || m.State() != "ready" {
			t.Fatalf("start: %v %s", err, m.State())
		}
		if err := m.Start(context.Background()); err != nil {
			t.Fatal("idempotent start", err)
		}
		if err := m.Stop(context.Background()); err != nil || m.State() != "stopped" {
			t.Fatalf("stop: %v %s", err, m.State())
		}
		if _, err := m.Status(context.Background()); status.Code(err) != codes.Unavailable {
			t.Fatal("stopped RPC available")
		}
		if err := m.Stop(context.Background()); err != nil {
			t.Fatal("idempotent stop", err)
		}
	}
	if starts.Load() != 3 {
		t.Fatal("unexpected additional child")
	}
	entries, err := os.ReadDir(c.StateDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("state directory not cleaned")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("closed manager restarted")
	}
}

func TestManagerOverlapAndCancelledStart(t *testing.T) {
	c := config(t)
	m, err := newManager(context.Background(), c, child("loading"), func(context.Context, string, string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()
	waitState(t, m, "starting")
	if err := m.Stop(context.Background()); status.Code(err) != codes.Aborted {
		t.Fatal("overlapping stop accepted", err)
	}
	if err := m.Start(context.Background()); status.Code(err) != codes.Aborted {
		t.Fatal("overlapping start accepted", err)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}
	if m.State() != "stopped" && m.State() != "failed" {
		t.Fatal("cancelled start child not joined")
	}
}

func TestManagerForcedStopAndCrash(t *testing.T) {
	for _, mode := range []string{"stuck", "crash"} {
		t.Run(mode, func(t *testing.T) {
			c := config(t)
			if mode == "stuck" {
				c.ShutdownTimeout = 100 * time.Millisecond
			}
			m, err := newManager(context.Background(), c, child(mode), func(context.Context, string, string) bool { return true })
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			if err := m.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			if mode == "stuck" {
				if err := m.Stop(context.Background()); err == nil {
					t.Fatal("forced kill not reported")
				}
			}
			waitState(t, m, "failed")
			if _, err := m.Status(context.Background()); status.Code(err) != codes.Unavailable {
				t.Fatal("failed backend RPC available")
			}
		})
	}
}
