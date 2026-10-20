package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

func controlRequest(s *Service, method, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestAdmissionDrainConcurrentRequestsAndResume(t *testing.T) {
	s, _ := authenticatedService(t, testSecret)
	started, release := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	s.rpc = &chatBackend{generate: func(ctx context.Context, _ *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
		if count.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return rpcbackend.Observation{}, ctx.Err()
		}
		if err := consume([]byte("ok")); err != nil {
			return rpcbackend.Observation{}, err
		}
		return success(1), nil
	}}
	s.requestTimeout = 5 * time.Second
	generate := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body(false)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+testSecret)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- generate() }()
	var once sync.Once
	defer once.Do(func() { close(release) })
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("generation did not start")
	}
	for i := 0; i < 2; i++ {
		w := controlRequest(s, "POST", "/internal/v1/admission/drain", testSecret)
		var v admissionState
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || w.Code != 200 || v.State != "draining" || v.ActiveGenerationHandlers != 1 || v.NativeIdle != nil {
			t.Fatalf("drain: %d %s err=%v", w.Code, w.Body, err)
		}
	}
	var attempts sync.WaitGroup
	for i := 0; i < 32; i++ {
		attempts.Add(1)
		go func() {
			defer attempts.Done()
			w := generate()
			if w.Code != 503 || !strings.Contains(w.Body.String(), "admission_draining") {
				t.Errorf("drain rejection: %d", w.Code)
			}
		}()
	}
	attempts.Wait()
	if count.Load() != 1 {
		t.Fatal("new request reached backend during drain")
	}
	if w := controlRequest(s, "GET", "/readyz", ""); w.Code != 503 {
		t.Fatal("readiness ignored drain")
	}
	if w := controlRequest(s, "GET", "/healthz", ""); w.Code != 200 {
		t.Fatal("health affected")
	}
	if w := controlRequest(s, "GET", "/v1/models", testSecret); w.Code != 200 {
		t.Fatal("discovery affected")
	}
	once.Do(func() { close(release) })
	w := <-done
	if w.Code != 200 {
		t.Fatalf("existing request cancelled by drain: %d %s", w.Code, w.Body)
	}
	if v := s.admissionSnapshot(); v.State != "drained" || v.ActiveGenerationHandlers != 0 || v.NativeIdle != nil {
		t.Fatal(v)
	}
	for i := 0; i < 2; i++ {
		if w := controlRequest(s, "POST", "/internal/v1/admission/resume", testSecret); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if w := generate(); w.Code != 200 || count.Load() != 2 {
		t.Fatal("resume failed")
	}
	if v := s.admissionSnapshot(); v.State != "accepting" || v.ActiveGenerationHandlers != 0 {
		t.Fatal(v)
	}
}

func TestAdmissionControlAuthAndRateOrder(t *testing.T) {
	s, _ := authenticatedService(t, "")
	for _, path := range []string{"/internal/v1/admission", "/internal/v1/admission/drain", "/internal/v1/admission/resume"} {
		method := "POST"
		if path == "/internal/v1/admission" {
			method = "GET"
		}
		if w := controlRequest(s, method, path, testSecret); w.Code != 403 {
			t.Fatal("unconfigured controls allowed")
		}
	}
	s.authDigest = tokenDigest(testSecret)
	if w := controlRequest(s, "POST", "/internal/v1/admission/drain", ""); w.Code != 401 || s.admissionSnapshot().State != "accepting" {
		t.Fatal("missing credentials changed state")
	}
	if w := controlRequest(s, "POST", "/internal/v1/admission/drain?extra=1", testSecret); w.Code != 400 {
		t.Fatal("unexpected query accepted")
	}
	s.limiter = newRequestLimiter(1, 1, time.Now())
	controlRequest(s, "POST", "/internal/v1/admission/drain", testSecret)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Body = unreadBody{t}
	r.Header.Set("Authorization", "Bearer "+testSecret)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 503 || s.limiter.take(time.Now()) != 0 {
		t.Fatal("drain rejection consumed credit")
	}
	if w := controlRequest(s, "POST", "/internal/v1/admission/resume", testSecret); w.Code != 200 {
		t.Fatal("rate limit blocked control")
	}
}
