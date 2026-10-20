package service

import (
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateBucketRefillAndCap(t *testing.T) {
	now := time.Now()
	l := newRequestLimiter(60, 2, now)
	if l.take(now) != 0 || l.take(now) != 0 || l.take(now) != 1 {
		t.Fatal("burst not enforced")
	}
	if l.take(now.Add(500*time.Millisecond)) != 1 || l.take(now.Add(time.Second)) != 0 {
		t.Fatal("fractional refill")
	}
	// Old timestamps cannot add credit or move the clock backwards.
	if l.take(now) != 1 {
		t.Fatal("clock moved backward")
	}
	late := now.Add(time.Hour)
	if l.take(late) != 0 || l.take(late) != 0 || l.take(late) != 1 {
		t.Fatal("idle credit exceeded burst")
	}
	slow := newRequestLimiter(1, 1, now)
	if slow.take(now) != 0 || slow.take(now) != 60 || slow.take(now.Add(1500*time.Millisecond)) != 59 || slow.take(now.Add(time.Minute)) != 0 {
		t.Fatal("Retry-After rounding/refill")
	}
}

func TestRateConcurrentAdmission(t *testing.T) {
	now := time.Now()
	l := newRequestLimiter(60, 7, now)
	var admitted atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 100; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if l.take(now) == 0 {
				admitted.Add(1)
			}
		}()
	}
	workers.Wait()
	if admitted.Load() != 7 {
		t.Fatalf("admitted %d, want 7", admitted.Load())
	}
}

func TestRateMiddlewareOrderAndNoBackendWork(t *testing.T) {
	s, b := authenticatedService(t, testSecret)
	s.limiter = newRequestLimiter(1, 1, time.Now())
	request := func(token string, stream bool, unread bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body(stream)))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if unread {
			r.Body = unreadBody{t}
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request("", false, true); w.Code != 401 {
		t.Fatal("authentication must precede rate limit")
	}
	if w := request(testSecret, false, false); w.Code != 200 {
		t.Fatalf("unauthorized request consumed credit: %d", w.Code)
	}
	id := s.ids.Load()
	for _, stream := range []bool{false, true} {
		w := request(testSecret, stream, true)
		if w.Code != 429 || w.Header().Get("Retry-After") != "60" || !strings.Contains(w.Body.String(), `"rate_limit_exceeded"`) || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("rate rejection: %d %s", w.Code, w.Body)
		}
	}
	if b.statusCalls != 1 || b.generationCalls != 1 || len(s.active) != 0 || s.ids.Load() != id {
		t.Fatal("rate-rejected request reached backend/admission")
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal("rate limit blocked health")
		}
	}
}

func TestRateConfiguration(t *testing.T) {
	for _, tc := range []struct {
		rate, burst int
		valid       bool
	}{{0, 0, true}, {1, 0, true}, {60000, 1024, true}, {-1, 0, false}, {60001, 0, false}, {0, 1, false}, {1, -1, false}, {1, 1025, false}} {
		c := Config{Listen: "127.0.0.1:8080", BackendGRPC: "127.0.0.1:8081", RatePerMinute: tc.rate, RateBurst: tc.burst}
		s, err := New(c)
		if (err == nil) != tc.valid {
			t.Fatalf("rate=%d burst=%d err=%v", tc.rate, tc.burst, err)
		}
		if s != nil {
			if (s.limiter == nil) != (tc.rate == 0) {
				t.Fatal("limiter enablement")
			}
			s.Close()
		}
	}
}
