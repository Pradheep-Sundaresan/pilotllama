package rpcbackend_test

import (
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/service"
)

// One explicitly opted-in real model process, reused across sequential requests.
func TestNativeRPC(t *testing.T) {
	binary, model := os.Getenv("INFERNO_NATIVE_RPC_SERVER"), os.Getenv("INFERNO_NATIVE_RPC_MODEL")
	if binary == "" || model == "" {
		t.Skip("opt in with scripts/test-native-rpc.sh LOCAL_GGUF")
	}
	f := startRPCProcess(t, binary, func(ready string) []string {
		return []string{"--model", model, "--listen", "127.0.0.1:0", "--model-id", "local-nemotron-4b", "--ready-file", ready}
	}, 30*time.Second)
	arithmetic := func(t *testing.T, id uint64) {
		t.Helper()
		var text []byte
		result, err := f.client.Generate(contextFor(t, 5*time.Second), &pb.GenerateRequest{RequestId: id, Prompt: "What is 2 + 2? Answer with one digit.", MaxOutputTokens: 1}, func(piece []byte) error { text = append(text, piece...); return nil })
		if err != nil || !result.TransportComplete || result.Finished.State != pb.TerminalState_TERMINAL_STATE_COMPLETED || result.ReceivedTokens != 1 || strings.TrimSpace(string(text)) != "4" || result.TTFTMS == nil || result.Finished.Metrics.TtftMs == nil {
			t.Fatalf("native response text=%q observation=%+v err=%v", text, result, err)
		}
	}
	t.Run("readiness", func(t *testing.T) {
		state, err := f.client.Status(contextFor(t, time.Second))
		if err != nil || state.Response.State != pb.BackendState_BACKEND_STATE_READY || state.Response.GetModelId() != "local-nemotron-4b" {
			t.Fatalf("status=%+v err=%v", state, err)
		}
		s, err := service.New(service.Config{Listen: "127.0.0.1:8080", BackendGRPC: f.target})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	})
	t.Run("real-generation", func(t *testing.T) { arithmetic(t, 1) })
	t.Run("public-HTTP-JSON-and-SSE", func(t *testing.T) {
		s, err := service.New(service.Config{Listen: "127.0.0.1:8080", BackendGRPC: f.target})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		h := httptest.NewServer(s.Handler())
		defer h.Close()
		for _, stream := range []bool{false, true} {
			checkChat(t, postChat(t, contextFor(t, 5*time.Second), h.URL, "local-nemotron-4b", "What is 2 + 2? Answer with one digit.", stream, 1), "4", stream)
		}
	})
	t.Run("active-cancel-and-recovery", func(t *testing.T) {
		ctx := contextFor(t, 5*time.Second)
		stream := open(t, f, 2, "Explain what a CPU does in a short paragraph.", ctx)
		first, err := stream.Recv()
		if err != nil || first.GetToken() == nil {
			t.Fatalf("first token=%v err=%v", first, err)
		}
		cancel, err := f.client.Cancel(ctx, 2)
		if err != nil || cancel.Disposition != pb.CancelDisposition_CANCEL_DISPOSITION_REQUESTED {
			t.Fatalf("cancel=%v err=%v", cancel, err)
		}
		result := terminal(t, stream)
		if result.State != pb.TerminalState_TERMINAL_STATE_CANCELLED || result.Error.Code != pb.ErrorCode_ERROR_CODE_CANCELLED || result.Metrics.OutputTokens < 1 {
			t.Fatalf("native cancel=%v", result)
		}
		arithmetic(t, 3)
	})
	t.Run("admitted-deadline-and-recovery", func(t *testing.T) {
		stream := open(t, f, 4, "Explain what a CPU does in a short paragraph.", contextFor(t, 50*time.Millisecond))
		for {
			_, err := stream.Recv()
			if err != nil {
				if status.Code(err) != codes.DeadlineExceeded {
					t.Fatalf("native deadline: %v", err)
				}
				break
			}
		}
		arithmetic(t, 5)
	})
	t.Run("disconnect-and-recovery", func(t *testing.T) {
		want := errors.New("native consumer disconnected")
		result, err := f.client.Generate(contextFor(t, 5*time.Second), request(6, "Explain what a CPU does in a short paragraph."), func([]byte) error { return want })
		if !errors.Is(err, want) || result.TransportComplete || result.ReceivedTokens != 1 || result.RPCCode != nil {
			t.Fatalf("native disconnect=%+v err=%v", result, err)
		}
		arithmetic(t, 7)
	})
	t.Run("backend-error-and-recovery", func(t *testing.T) {
		result, err := f.client.Generate(contextFor(t, 5*time.Second), request(8, strings.Repeat(" hello", 5000)), nil)
		if err != nil || !result.TransportComplete || result.Finished.State != pb.TerminalState_TERMINAL_STATE_FAILED || result.Finished.Error.Code != pb.ErrorCode_ERROR_CODE_BACKEND_FAILURE || result.ReceivedTokens != 0 {
			t.Fatalf("native failure=%+v err=%v", result, err)
		}
		arithmetic(t, 9)
	})
	// The process helper sends SIGTERM and checks exit 0 after all native requests.
}
