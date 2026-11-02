package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

func waitForQueueSize(t *testing.T, s *Service, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.queueMu.Lock()
		got := len(s.waiters)
		s.queueMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue did not reach %d", want)
}

func TestPhase12DTwoThousandOverloadDecisions(t *testing.T) {
	s := &Service{active: make(chan struct{}, 2), queueCapacity: 8, queueWaitTimeout: time.Second,
		waiters: make(map[uint64]time.Time), drainSignal: make(chan struct{})}
	s.active <- struct{}{}
	s.active <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waiters sync.WaitGroup
	for id := uint64(1); id <= 8; id++ {
		waiters.Add(1)
		go func(id uint64) {
			defer waiters.Done()
			s.acquireGeneration(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx), id)
		}(id)
	}
	waitForQueueSize(t, s, 8)
	for id := uint64(9); id < 2009; id++ {
		w := httptest.NewRecorder()
		if _, ok := s.acquireGeneration(w, httptest.NewRequest("POST", "/v1/chat/completions", nil), id); ok || w.Code != 429 {
			t.Fatalf("overload decision id=%d status=%d", id, w.Code)
		}
	}
	if s.queueFull.Load() != 2000 {
		t.Fatalf("queue-full=%d", s.queueFull.Load())
	}
	cancel()
	waiters.Wait()
	waitForQueueSize(t, s, 0)
}

func TestPhase12DDisconnectStormReleasesAndRecovers(t *testing.T) {
	s := &Service{active: make(chan struct{}, 2), queueCapacity: 64, queueWaitTimeout: 2 * time.Second,
		waiters: make(map[uint64]time.Time), drainSignal: make(chan struct{})}
	s.active <- struct{}{}
	s.active <- struct{}{}
	type result struct {
		id uint64
		ok bool
	}
	results := make(chan result, 50)
	cancels := make([]context.CancelFunc, 50)
	var workers sync.WaitGroup
	for i := range 50 {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		workers.Add(1)
		go func(id uint64) {
			defer workers.Done()
			_, ok := s.acquireGeneration(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", nil).WithContext(ctx), id)
			results <- result{id, ok}
		}(uint64(i + 1))
	}
	waitForQueueSize(t, s, 50)
	for i := range 30 {
		cancels[i]()
	}
	waitForQueueSize(t, s, 20)
	<-s.active
	completed, canceled := 0, 0
	for completed < 20 {
		got := <-results
		if got.ok {
			completed++
			<-s.active
		} else {
			canceled++
		}
	}
	<-s.active
	workers.Wait()
	close(results)
	for got := range results {
		if !got.ok {
			canceled++
		}
	}
	if canceled != 30 || s.queueCanceled.Load() != 30 || len(s.active) != 0 {
		t.Fatalf("canceled=%d counter=%d active=%d", canceled, s.queueCanceled.Load(), len(s.active))
	}
	waitForQueueSize(t, s, 0)
}

func TestPhase12DDeadlineCascadeNeverStartsExpiredWork(t *testing.T) {
	var backendCalls atomic.Int32
	backend := &chatBackend{generate: func(ctx context.Context, _ *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
		backendCalls.Add(1)
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return rpcbackend.Observation{}, ctx.Err()
		}
		if err := consume([]byte("OK")); err != nil {
			return rpcbackend.Observation{}, err
		}
		return success(1), nil
	}}
	s := chatService(backend)
	s.active, s.queueCapacity, s.queueWaitTimeout = make(chan struct{}, 2), 8, 500*time.Millisecond
	s.waiters, s.drainSignal = make(map[uint64]time.Time), make(chan struct{})
	start := make(chan struct{})
	statuses := make(chan int, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body(false)))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			statuses <- w.Code
		}()
	}
	close(start)
	workers.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 4 || counts[504] != 4 || backendCalls.Load() != 4 || s.queueExpired.Load() != 4 {
		t.Fatalf("statuses=%v backend_calls=%d expired=%d", counts, backendCalls.Load(), s.queueExpired.Load())
	}
	waitForQueueSize(t, s, 0)
}

func TestPhase12DDrainReleasesBurstAndResumeRecovers(t *testing.T) {
	s := &Service{active: make(chan struct{}, 2), queueCapacity: 20, queueWaitTimeout: time.Second,
		waiters: make(map[uint64]time.Time), drainSignal: make(chan struct{})}
	s.active <- struct{}{}
	s.active <- struct{}{}
	results := make(chan int, 20)
	var workers sync.WaitGroup
	for id := uint64(1); id <= 20; id++ {
		workers.Add(1)
		go func(id uint64) {
			defer workers.Done()
			w := httptest.NewRecorder()
			s.acquireGeneration(w, httptest.NewRequest("POST", "/v1/chat/completions", nil), id)
			results <- w.Code
		}(id)
	}
	waitForQueueSize(t, s, 20)
	s.setDraining(httptest.NewRecorder(), httptest.NewRequest("POST", "/internal/v1/admission/drain", nil), true)
	workers.Wait()
	close(results)
	for status := range results {
		if status != 503 {
			t.Fatalf("drained waiter status=%d", status)
		}
	}
	waitForQueueSize(t, s, 0)
	s.setDraining(httptest.NewRecorder(), httptest.NewRequest("POST", "/internal/v1/admission/resume", nil), false)
	<-s.active
	<-s.active
	if _, ok := s.acquireGeneration(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", nil), 21); !ok {
		t.Fatal("admission did not recover after resume")
	}
	<-s.active
}
