package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

type chatBackend struct {
	generate  func(context.Context, *pb.GenerateRequest, func([]byte) error) (rpcbackend.Observation, error)
	state     pb.BackendState
	scheduler *pb.SchedulerSnapshot
	memory    *pb.MemorySnapshot
}

func (b *chatBackend) Close() error { return nil }
func (b *chatBackend) Status(context.Context) (rpcbackend.StatusObservation, error) {
	model := "test-model"
	state := b.state
	if state == 0 {
		state = pb.BackendState_BACKEND_STATE_READY
	}
	return rpcbackend.StatusObservation{Response: &pb.GetStatusResponse{State: state, ModelId: &model, Scheduler: b.scheduler, Memory: b.memory}}, nil
}
func (b *chatBackend) Generate(ctx context.Context, r *pb.GenerateRequest, f func([]byte) error) (rpcbackend.Observation, error) {
	return b.generate(ctx, r, f)
}
func chatService(b *chatBackend) *Service {
	return &Service{rpc: b, active: make(chan struct{}, 4), requestTimeout: time.Second}
}
func body(stream bool) string {
	if stream {
		return `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"max_tokens":4,"stream":true}`
	}
	return `{"model":"test-model","messages":[{"role":"user","content":"hi"}],"max_tokens":4}`
}
func invoke(s *Service, data string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func success(n uint32) rpcbackend.Observation {
	return rpcbackend.Observation{TransportComplete: true, Finished: &pb.Finished{State: pb.TerminalState_TERMINAL_STATE_COMPLETED, Metrics: &pb.RequestMetrics{OutputTokens: n}}}
}

func TestChatJSONAndSSE(t *testing.T) {
	backend := &chatBackend{generate: func(ctx context.Context, r *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing outgoing budget")
		}
		if r.RequestId == 0 || r.Prompt != "hi" || r.MaxOutputTokens != 4 {
			t.Error("request mapping")
		}
		for _, piece := range [][]byte{{0xf0, 0x9f}, {0x98, 0x80}, nil, {'!'}} {
			if err := consume(piece); err != nil {
				return rpcbackend.Observation{}, err
			}
		}
		return success(4), nil
	}}
	s := chatService(backend)
	w := invoke(s, body(false))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	choice := response["choices"].([]any)[0].(map[string]any)
	if response["object"] != "chat.completion" || response["id"] != "chatcmpl-"+w.Header().Get("X-Request-ID") || choice["message"].(map[string]any)["content"] != "😀!" || choice["finish_reason"] != "length" || response["usage"] != nil {
		t.Fatal(response)
	}
	w = invoke(s, body(true))
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/event-stream" || !w.Flushed {
		t.Fatal(w.Code, w.Header())
	}
	var text strings.Builder
	done, finishes, roles := 0, 0, 0
	for _, record := range strings.Split(strings.TrimSpace(w.Body.String()), "\n\n") {
		data := strings.TrimPrefix(record, "data: ")
		if data == "[DONE]" {
			done++
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatal(err)
		}
		if chunk["object"] != "chat.completion.chunk" || chunk["id"] != "chatcmpl-"+w.Header().Get("X-Request-ID") || chunk["usage"] != nil {
			t.Fatal(chunk)
		}
		choice := chunk["choices"].([]any)[0].(map[string]any)
		delta := choice["delta"].(map[string]any)
		if delta["role"] == "assistant" {
			roles++
		}
		if value, ok := delta["content"].(string); ok {
			text.WriteString(value)
		}
		if choice["finish_reason"] != nil {
			if choice["finish_reason"] != "length" {
				t.Fatal(choice)
			}
			finishes++
		}
	}
	if text.String() != "😀!" || done != 1 || finishes != 1 || roles != 1 {
		t.Fatal(text.String(), done, finishes, roles)
	}
}

func TestChatRequestValidation(t *testing.T) {
	backend := &chatBackend{generate: func(context.Context, *pb.GenerateRequest, func([]byte) error) (rpcbackend.Observation, error) {
		t.Fatal("invalid request reached generation")
		return success(0), nil
	}}
	for _, tc := range []struct {
		name, data string
		code       int
	}{
		{"malformed", "{", 400}, {"trailing", body(false) + "{}", 400}, {"empty", "{}", 400},
		{"role", strings.Replace(body(false), `"user"`, `"system"`, 1), 400},
		{"multiple", `{"model":"test-model","messages":[{"role":"user","content":"a"},{"role":"user","content":"b"}]}`, 400},
		{"sampling", strings.Replace(body(false), `"max_tokens":4`, `"temperature":0.5`, 1), 400},
		{"tools", strings.Replace(body(false), `"max_tokens":4`, `"tools":[]`, 1), 400},
		{"usage", strings.Replace(body(false), `"max_tokens":4`, `"stream_options":{"include_usage":true}`, 1), 400},
		{"zero-limit", strings.Replace(body(false), `"max_tokens":4`, `"max_tokens":0`, 1), 400},
		{"both-limits", strings.Replace(body(false), `"max_tokens":4`, `"max_tokens":4,"max_completion_tokens":4`, 1), 400},
		{"model", strings.Replace(body(false), `"test-model"`, `"other"`, 1), 404},
		{"too-large", strings.Repeat(" ", bodyLimit+1), 413},
		{"raw-UTF8", string([]byte{0xff}), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := invoke(chatService(backend), tc.data)
			if w.Code != tc.code || !strings.Contains(w.Body.String(), `"error"`) {
				t.Fatal(w.Code, w.Body)
			}
		})
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body(false)))
	w := httptest.NewRecorder()
	chatService(backend).Handler().ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
}

func TestChatUTF8AndPartialFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pieces [][]byte
		rpcErr error
	}{
		{"invalid", [][]byte{{0xff}}, nil}, {"incomplete", [][]byte{{0xf0}}, nil},
		{"partial-invalid", [][]byte{[]byte("ok"), {0xff}}, nil},
		{"partial-rpc-error", [][]byte{[]byte("ok")}, status.Error(codes.Internal, "secret backend path")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &chatBackend{generate: func(_ context.Context, _ *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
				for _, p := range tc.pieces {
					if err := consume(p); err != nil {
						return rpcbackend.Observation{}, err
					}
				}
				return success(uint32(len(tc.pieces))), tc.rpcErr
			}}
			w := invoke(chatService(backend), body(true))
			if strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), `"finish_reason":"`) || strings.Contains(w.Body.String(), "secret") || !strings.Contains(w.Body.String(), `"error"`) {
				t.Fatal(w.Code, w.Body)
			}
			if strings.HasPrefix(tc.name, "partial") {
				if w.Code != 200 {
					t.Fatal(w.Code)
				}
			} else if w.Code != 502 {
				t.Fatal(w.Code)
			}
		})
	}
}

func TestChatNativeFailureAndLimits(t *testing.T) {
	for _, code := range []codes.Code{codes.ResourceExhausted, codes.Unavailable, codes.DeadlineExceeded} {
		backend := &chatBackend{generate: func(context.Context, *pb.GenerateRequest, func([]byte) error) (rpcbackend.Observation, error) {
			return rpcbackend.Observation{}, status.Error(code, "private")
		}}
		want := map[codes.Code]int{codes.ResourceExhausted: 429, codes.Unavailable: 503, codes.DeadlineExceeded: 504}[code]
		if w := invoke(chatService(backend), body(true)); w.Code != want || strings.Contains(w.Body.String(), "private") {
			t.Fatal(w.Code, w.Body)
		}
	}
	backend := &chatBackend{generate: func(_ context.Context, _ *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
		for i := 0; i < 17; i++ {
			if err := consume([]byte(strings.Repeat("x", 65536))); err != nil {
				return rpcbackend.Observation{}, err
			}
		}
		return success(17), nil
	}}
	if w := invoke(chatService(backend), body(false)); w.Code != 502 || !strings.Contains(w.Body.String(), "response_too_large") {
		t.Fatal(w.Code)
	}
	backend.generate = func(context.Context, *pb.GenerateRequest, func([]byte) error) (rpcbackend.Observation, error) {
		r := success(0)
		r.Finished.State = pb.TerminalState_TERMINAL_STATE_FAILED
		return r, nil
	}
	if w := invoke(chatService(backend), body(false)); w.Code != 502 {
		t.Fatal(w.Code, w.Body)
	}
	backend.state = pb.BackendState_BACKEND_STATE_NO_MODEL
	if w := invoke(chatService(backend), body(false)); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestChatAdmissionCancellationAndTimeout(t *testing.T) {
	entered := make(chan struct{}, 4)
	backend := &chatBackend{generate: func(ctx context.Context, _ *pb.GenerateRequest, _ func([]byte) error) (rpcbackend.Observation, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return rpcbackend.Observation{}, ctx.Err()
	}}
	s := chatService(backend)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body(false))).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			s.Handler().ServeHTTP(httptest.NewRecorder(), r)
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			cancel()
			group.Wait()
			t.Fatal("request did not enter")
		}
	}
	if w := invoke(s, body(false)); w.Code != 429 {
		t.Error(w.Code)
	}
	health := httptest.NewRecorder()
	s.Handler().ServeHTTP(health, httptest.NewRequest("GET", "/healthz", nil))
	if health.Code != 200 {
		t.Error(health.Code)
	}
	cancel()
	group.Wait()
	s.requestTimeout = 10 * time.Millisecond
	if w := invoke(s, body(false)); w.Code != 504 {
		t.Fatal(w.Code, w.Body)
	}
}

type failedWriter struct {
	header http.Header
	writes int
}

func (w *failedWriter) Header() http.Header { return w.header }
func (*failedWriter) WriteHeader(int)       {}
func (w *failedWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("client disconnected")
}
func (*failedWriter) Flush() {}
func TestChatWriteFailureCancelsContext(t *testing.T) {
	var captured context.Context
	backend := &chatBackend{generate: func(ctx context.Context, _ *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
		captured = ctx
		return rpcbackend.Observation{}, consume([]byte("x"))
	}}
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body(true)))
	r.Header.Set("Content-Type", "application/json")
	w := &failedWriter{header: make(http.Header)}
	chatService(backend).Handler().ServeHTTP(w, r)
	if captured == nil || captured.Err() == nil || w.writes != 1 {
		t.Fatal("write failure did not stop/cancel request")
	}
}
