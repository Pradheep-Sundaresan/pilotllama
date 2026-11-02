package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

func queueFixture(wait time.Duration) *Service {
	s := &Service{active: make(chan struct{}, 2), queueCapacity: 1, queueWaitTimeout: wait,
		waiters: make(map[uint64]time.Time), drainSignal: make(chan struct{})}
	s.active <- struct{}{}
	s.active <- struct{}{}
	return s
}

func awaitWaiter(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.queueMu.Lock()
		n := len(s.waiters)
		s.queueMu.Unlock()
		if n == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("request did not enter queue")
}

func TestQueueFullRetryAndCancel(t *testing.T) {
	s := queueFixture(200 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	done := make(chan bool, 1)
	go func() { _, ok := s.acquireGeneration(recorder, req, 1); done <- ok }()
	awaitWaiter(t, s)
	if recorder.Header().Get("X-Queue-Position") != "1" {
		t.Fatal("missing queue position")
	}
	full := httptest.NewRecorder()
	if _, ok := s.acquireGeneration(full, httptest.NewRequest("POST", "/v1/chat/completions", nil), 2); ok || full.Code != 429 {
		t.Fatal(full.Code)
	}
	retry, err := strconv.Atoi(full.Header().Get("Retry-After"))
	if err != nil || retry < 1 {
		t.Fatal("missing calculated retry delay")
	}
	cancel()
	if <-done {
		t.Fatal("canceled waiter acquired slot")
	}
	s.queueMu.Lock()
	remaining := len(s.waiters)
	s.queueMu.Unlock()
	if remaining != 0 {
		t.Fatal("canceled waiter retained queue slot")
	}
	if s.queueFull.Load() != 1 || s.queueCanceled.Load() != 1 {
		t.Fatal("queue rejection taxonomy not recorded")
	}
}

func TestQueueWaitDeadlineAndPromotion(t *testing.T) {
	s := queueFixture(30 * time.Millisecond)
	expired := httptest.NewRecorder()
	if _, ok := s.acquireGeneration(expired, httptest.NewRequest("POST", "/v1/chat/completions", nil), 1); ok || expired.Code != 504 {
		t.Fatal(expired.Code)
	}
	s.queueMu.Lock()
	remaining := len(s.waiters)
	s.queueMu.Unlock()
	if remaining != 0 {
		t.Fatal("expired waiter retained queue slot")
	}
	if s.queueExpired.Load() != 1 {
		t.Fatal("queue expiry not recorded")
	}
	s.queueWaitTimeout = time.Second
	promoted := httptest.NewRecorder()
	done := make(chan bool, 1)
	go func() {
		_, ok := s.acquireGeneration(promoted, httptest.NewRequest("POST", "/v1/chat/completions", nil), 2)
		done <- ok
	}()
	awaitWaiter(t, s)
	<-s.active
	if !<-done {
		t.Fatal("waiter did not acquire freed slot")
	}
	<-s.active
}

func TestQueueDrainReleasesWaiter(t *testing.T) {
	s := queueFixture(time.Second)
	recorder := httptest.NewRecorder()
	done := make(chan bool, 1)
	go func() {
		_, ok := s.acquireGeneration(recorder, httptest.NewRequest("POST", "/v1/chat/completions", nil), 1)
		done <- ok
	}()
	awaitWaiter(t, s)
	s.setDraining(httptest.NewRecorder(), httptest.NewRequest("POST", "/internal/v1/admission/drain", nil), true)
	if <-done || recorder.Code != 503 {
		t.Fatal("drain did not release waiter")
	}
}

func TestHTTPQueueAdmitsSixAndRejectsExcess(t *testing.T) {
	release := make(chan struct{})
	backend := &chatBackend{generate: func(ctx context.Context, _ *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return rpcbackend.Observation{}, ctx.Err()
		}
		if err := consume([]byte("OK")); err != nil {
			return rpcbackend.Observation{}, err
		}
		return success(1), nil
	}}
	s := chatService(backend)
	s.active, s.queueCapacity, s.queueWaitTimeout = make(chan struct{}, 2), 4, time.Second
	s.waiters, s.drainSignal = make(map[uint64]time.Time), make(chan struct{})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	start := make(chan struct{})
	type result struct {
		status        int
		queuePosition string
		retryAfter    string
	}
	results := make(chan result, 8)
	var finished atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body(false)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				results <- result{status: -1}
				finished.Add(1)
				return
			}
			results <- result{
				status:        resp.StatusCode,
				queuePosition: resp.Header.Get("X-Queue-Position"),
				retryAfter:    resp.Header.Get("Retry-After"),
			}
			resp.Body.Close()
			finished.Add(1)
		}()
	}
	close(start)
	deadline := time.Now().Add(time.Second)
	ready := false
	for time.Now().Before(deadline) {
		s.queueMu.Lock()
		waiting := len(s.waiters)
		s.queueMu.Unlock()
		if waiting == 4 && finished.Load() >= 2 {
			ready = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ready {
		t.Fatal("burst did not reach two active, four waiting and two rejected")
	}
	close(release)
	wg.Wait()
	close(results)
	counts := map[int]int{}
	queued, retried := 0, 0
	for got := range results {
		counts[got.status]++
		if got.status == http.StatusOK && got.queuePosition != "" {
			position, err := strconv.Atoi(got.queuePosition)
			if err != nil || position < 1 || position > 4 {
				t.Fatalf("invalid queue position %q", got.queuePosition)
			}
			queued++
		}
		if got.status == http.StatusTooManyRequests {
			delay, err := strconv.Atoi(got.retryAfter)
			if err != nil || delay < 1 {
				t.Fatalf("invalid Retry-After %q", got.retryAfter)
			}
			retried++
		}
	}
	if counts[200] != 6 || counts[429] != 2 || counts[-1] != 0 || queued != 4 || retried != 2 {
		t.Fatal(counts)
	}
	if s.queueFull.Load() != 2 {
		t.Fatalf("queue-full counter=%d", s.queueFull.Load())
	}
	records := s.history.snapshot()
	waited := 0
	for _, record := range records {
		if record.GoQueueWaitMS > 0 {
			waited++
		}
	}
	if len(records) != 6 || waited != 4 {
		t.Fatalf("records=%d waited=%d", len(records), waited)
	}
}
