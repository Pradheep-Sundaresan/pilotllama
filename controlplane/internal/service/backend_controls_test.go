package service

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type controlBackend struct {
	authBackend
	starts, stops    atomic.Int32
	entered, release chan struct{}
}

func (b *controlBackend) State() string { return "stopped" }
func (b *controlBackend) Start(ctx context.Context) error {
	b.starts.Add(1)
	if b.entered != nil {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (b *controlBackend) Stop(context.Context) error { b.stops.Add(1); return nil }

func TestBackendControlSafetyAndOverlap(t *testing.T) {
	s, _ := authenticatedService(t, testSecret)
	if w := controlRequest(s, "POST", "/internal/v1/backend/stop", testSecret); w.Code != 409 {
		t.Fatal("external backend controllable")
	}
	b := &controlBackend{entered: make(chan struct{}), release: make(chan struct{})}
	s.managed = b
	if w := controlRequest(s, "POST", "/internal/v1/backend/stop", ""); w.Code != 401 || b.stops.Load() != 0 {
		t.Fatal("control auth bypassed")
	}
	s.generationHandlers = 1
	if w := controlRequest(s, "POST", "/internal/v1/backend/stop", testSecret); w.Code != 409 || b.stops.Load() != 0 || s.admissionSnapshot().State != "draining" {
		t.Fatal("active generation stopped")
	}
	s.generationHandlers = 0
	if w := controlRequest(s, "POST", "/internal/v1/backend/stop", testSecret); w.Code != 200 || b.stops.Load() != 1 {
		t.Fatal("stop failed")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- controlRequest(s, "POST", "/internal/v1/backend/start", testSecret) }()
	<-b.entered
	for _, path := range []string{"/internal/v1/backend/start", "/internal/v1/backend/stop", "/internal/v1/admission/resume"} {
		if w := controlRequest(s, "POST", path, testSecret); w.Code != 409 {
			t.Errorf("overlap %s accepted", path)
		}
	}
	if w := controlRequest(s, "GET", "/healthz", ""); w.Code != 200 {
		t.Error("health blocked during start")
	}
	if w := controlRequest(s, "GET", "/internal/v1/backend", testSecret); w.Code != 200 {
		t.Error("status blocked during start")
	}
	close(b.release)
	if w := <-done; w.Code != 200 || s.admissionSnapshot().State != "drained" {
		t.Fatal("start did not preserve drain")
	}
}
