package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

func TestRequestHistoryCaptureAndPrivacy(t *testing.T) {
	s, _ := authenticatedService(t, testSecret)
	s.rpc = &chatBackend{generate: func(ctx context.Context, _ *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
		if err := consume([]byte("private-output")); err != nil {
			return rpcbackend.Observation{}, err
		}
		v := success(1)
		v.ReceivedTokens, v.TokenBytes = 1, 14
		return v, nil
	}}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(strings.Replace(body(false), "hi", "private-prompt", 1)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+testSecret)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	id := w.Header().Get("X-Request-ID")
	if v := controlRequest(s, "GET", "/internal/v1/requests", ""); v.Code != 401 {
		t.Fatal("history not authenticated")
	}
	v := controlRequest(s, "GET", "/internal/v1/requests/"+id, testSecret)
	var record requestRecord
	if err := json.Unmarshal(v.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if v.Code != 200 || record.RequestID != id || record.Outcome != "completed" || record.Native == nil || record.Native.OutputTokens != 1 || record.ReceivedTokens != 1 || record.Native.TTFTMS != nil || record.RPCTTFTMS != nil || record.StreamWriteFailed != nil || record.HandlerLatencyMS < 0 {
		t.Fatalf("unexpected record %s", v.Body)
	}
	for _, secret := range []string{testSecret, "private-prompt", "private-output"} {
		if strings.Contains(v.Body.String(), secret) {
			t.Fatal("payload/secret retained")
		}
	}
	if v := controlRequest(s, "GET", "/internal/v1/requests/missing", testSecret); v.Code != 404 {
		t.Fatal("missing record")
	}
	// Failed RPCs retain absent measurements; no raw error text enters history.
	s.rpc = &chatBackend{generate: func(context.Context, *pb.GenerateRequest, func([]byte) error) (rpcbackend.Observation, error) {
		return rpcbackend.Observation{}, errors.New("private-error")
	}}
	r = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body(true)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+testSecret)
	s.Handler().ServeHTTP(httptest.NewRecorder(), r)
	record = s.history.snapshot()[0]
	if record.Outcome != "failed" || record.Native != nil || record.NativeState != nil || record.RPCCode != nil || record.StreamWriteFailed == nil || *record.StreamWriteFailed {
		t.Fatal("failed RPC measurements fabricated")
	}
	before := len(s.history.snapshot())
	controlRequest(s, "POST", "/v1/chat/completions", testSecret) // Invalid body: no RPC.
	if len(s.history.snapshot()) != before {
		t.Fatal("pre-RPC rejection recorded as inference")
	}
}

func TestHistoryBoundedConcurrentAndCopied(t *testing.T) {
	var h requestHistory
	var workers sync.WaitGroup
	for i := 0; i < 256; i++ {
		workers.Add(1)
		go func(i int) { defer workers.Done(); h.add(requestRecord{RequestID: fmt.Sprint(i)}); _ = h.snapshot() }(i)
	}
	workers.Wait()
	if len(h.snapshot()) != requestHistoryCapacity {
		t.Fatal("unbounded history")
	}
	h.add(requestRecord{RequestID: "newest"})
	if h.snapshot()[0].RequestID != "newest" {
		t.Fatal("history ordering")
	}
	duration := 2.0
	code := codes.OK
	v := makeRecord(1, time.Now(), "completed", rpcbackend.Observation{LatencyMS: &duration, RPCCode: &code}, nil)
	duration = 900
	if *v.RPCLatencyMS != 2 || *v.RPCCode != "OK" {
		t.Fatal("retained caller-owned data")
	}
	var ordered requestHistory
	for i := 0; i < 129; i++ {
		ordered.add(requestRecord{RequestID: fmt.Sprint(i)})
	}
	if records := ordered.snapshot(); records[0].RequestID != "128" || records[127].RequestID != "1" {
		t.Fatal("oldest entry not evicted")
	}
}
