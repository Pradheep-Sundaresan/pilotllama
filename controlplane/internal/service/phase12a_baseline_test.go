package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

// TestPhase12ABurstBaseline is a short model-free HTTP baseline, not an inference benchmark.
func TestPhase12ABurstBaseline(t *testing.T) {
	for _, clients := range []int{10, 25, 50, 100} {
		t.Run(fmt.Sprint(clients), func(t *testing.T) {
			backend := &chatBackend{generate: func(ctx context.Context, _ *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
				timer := time.NewTimer(80 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return rpcbackend.Observation{}, ctx.Err()
				}
				if err := consume([]byte("OK")); err != nil {
					return rpcbackend.Observation{}, err
				}
				return success(1), nil
			}}
			s := chatService(backend)
			s.requestTimeout = 2 * time.Second
			server := httptest.NewServer(s.Handler())
			defer server.Close()
			transport := &http.Transport{MaxIdleConns: clients, MaxIdleConnsPerHost: clients}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := make(chan struct{})
			statuses := make([]int, clients)
			durations := make([]time.Duration, clients)
			var wg sync.WaitGroup
			for i := range statuses {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					<-start
					began := time.Now()
					req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(body(false)))
					if err != nil {
						statuses[index] = -1
						return
					}
					req.Header.Set("Content-Type", "application/json")
					resp, err := client.Do(req)
					durations[index] = time.Since(began)
					if err != nil {
						statuses[index] = -1
						return
					}
					statuses[index] = resp.StatusCode
					resp.Body.Close()
				}(i)
			}
			close(start)
			wg.Wait()
			runtime.ReadMemStats(&after)
			counts := map[int]int{}
			for _, status := range statuses {
				counts[status]++
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			p95 := durations[(95*clients-1)/100]
			t.Logf("clients=%d status_counts=%v p95_response=%s heap_before=%d heap_after=%d", clients, counts, p95, before.HeapAlloc, after.HeapAlloc)
			if counts[-1] != 0 || counts[http.StatusOK]+counts[http.StatusTooManyRequests]+counts[http.StatusServiceUnavailable] != clients {
				t.Fatalf("unexpected outcomes: %v", counts)
			}
		})
	}
}
