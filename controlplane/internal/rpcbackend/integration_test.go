package rpcbackend_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
	"inferno.local/controlplane/internal/service"
)

type fixture struct {
	client  *rpcbackend.Client
	raw     pb.InferenceServiceClient
	process *os.Process
	target  string
}

func startFixture(t *testing.T, unready bool) *fixture {
	t.Helper()
	binary := os.Getenv("INFERNO_RPC_FIXTURE")
	if binary == "" {
		t.Skip("set INFERNO_RPC_FIXTURE to the built model-free C++ fixture")
	}
	return startRPCProcess(t, binary, func(ready string) []string {
		args := []string{ready}
		if unready {
			args = append(args, "unready")
		}
		return args
	}, 4*time.Second)
}

func startRPCProcess(t *testing.T, binary string, arguments func(string) []string, startup time.Duration) *fixture {
	t.Helper()
	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	logPath := filepath.Join(directory, "server.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, arguments(ready)...)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(done) }()
	var client *rpcbackend.Client
	var conn *grpc.ClientConn
	t.Cleanup(func() {
		if client != nil {
			_ = client.Close()
		}
		if conn != nil {
			_ = conn.Close()
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("fixture shutdown exceeded seven seconds")
		}
		_ = log.Close()
		if exitErr != nil {
			data, _ := os.ReadFile(logPath)
			t.Errorf("fixture exit: %v: %s", exitErr, data)
		}
	})
	deadline := time.NewTimer(startup)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var target string
wait:
	for {
		select {
		case <-done:
			data, _ := os.ReadFile(logPath)
			t.Fatalf("fixture failed: %v: %s", exitErr, data)
		case <-deadline.C:
			t.Fatal("fixture startup timeout")
		case <-ticker.C:
			if data, err := os.ReadFile(ready); err == nil {
				port, err := strconv.Atoi(strings.TrimSpace(string(data)))
				if err == nil && port > 0 {
					target = "127.0.0.1:" + strconv.Itoa(port)
					break wait
				}
			}
		}
	}
	client, err = rpcbackend.New(target)
	if err != nil {
		t.Fatal(err)
	}
	conn, err = grpc.NewClient("passthrough:///"+target, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy(), grpc.WithDisableRetry(), grpc.WithDisableServiceConfig(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(rpcbackend.MessageLimit), grpc.MaxCallSendMsgSize(rpcbackend.MessageLimit)))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{client, pb.NewInferenceServiceClient(conn), cmd.Process, target}
}
func request(id uint64, prompt string) *pb.GenerateRequest {
	return &pb.GenerateRequest{RequestId: id, Prompt: prompt, MaxOutputTokens: 8}
}
func contextFor(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
func open(t *testing.T, f *fixture, id uint64, prompt string, ctx context.Context) grpc.ServerStreamingClient[pb.GenerateEvent] {
	t.Helper()
	stream, err := f.raw.Generate(ctx, request(id, prompt))
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if err != nil || event.GetAccepted() == nil || event.RequestId != id {
		t.Fatalf("accepted: %v %v", event, err)
	}
	return stream
}
func terminal(t *testing.T, stream grpc.ServerStreamingClient[pb.GenerateEvent]) *pb.Finished {
	t.Helper()
	var result *pb.Finished
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.GetFinished() != nil {
			if result != nil {
				t.Fatal("duplicate terminal")
			}
			result = event.GetFinished()
		}
	}
	if result == nil {
		t.Fatal("missing terminal result")
	}
	return result
}
func gate(t *testing.T, f *fixture) grpc.ServerStreamingClient[pb.GenerateEvent] {
	t.Helper()
	stream := open(t, f, 1, "gate", contextFor(t, 3*time.Second))
	event, err := stream.Recv()
	if err != nil || string(event.GetToken().GetPiece()) != "G" {
		t.Fatalf("gate entry: %v %v", event, err)
	}
	return stream
}

func TestIntegrationStatusStreamingAndHTTPReadiness(t *testing.T) {
	f := startFixture(t, false)
	ctx := contextFor(t, 2*time.Second)
	state, err := f.client.Status(ctx)
	if err != nil || state.Response.State != pb.BackendState_BACKEND_STATE_READY || state.Response.GetScheduler().GetQueueCapacity() != 1 {
		t.Fatal(err)
	}
	var text []byte
	result, err := f.client.Generate(ctx, request(^uint64(0), "tokens"), func(piece []byte) error { text = append(text, piece...); return nil })
	if err != nil || !result.TransportComplete || result.Finished.State != pb.TerminalState_TERMINAL_STATE_COMPLETED || result.ReceivedTokens != 4 || result.TokenBytes != 5 || string(text) != "😀!" || result.TTFTMS == nil || result.LatencyMS == nil || result.ReceiveTokensPerSecond == nil || result.Finished.Metrics.TtftMs == nil {
		t.Fatalf("stream=%+v text=%q err=%v", result, text, err)
	}
	const token = "local-integration-secret-0123456789"
	s, err := service.New(service.Config{Listen: "127.0.0.1:8080", BackendGRPC: f.target, AuthToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, path := range []string{"/healthz", "/readyz"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/internal/v1/scheduler", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	s.Handler().ServeHTTP(w, req)
	var scheduler struct {
		Source            string `json:"source"`
		WaitingRequests   uint32 `json:"waiting_requests"`
		ExecutingRequests uint32 `json:"executing_requests"`
		QueueCapacity     uint32 `json:"queue_capacity"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &scheduler); err != nil || w.Code != 200 ||
		scheduler.Source != "cpp_runtime_status_snapshot" || scheduler.WaitingRequests != 0 ||
		scheduler.ExecutingRequests != 0 || scheduler.QueueCapacity != 1 {
		t.Fatalf("scheduler status=%d body=%s parsed=%+v err=%v", w.Code, w.Body, scheduler, err)
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/internal/v1/memory", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	s.Handler().ServeHTTP(w, req)
	var memory struct {
		Source           string  `json:"source"`
		ModelTensorBytes *uint64 `json:"model_tensor_bytes"`
		ProcessSamples   []struct {
			Point         string  `json:"point"`
			ResidentBytes *uint64 `json:"resident_bytes"`
		} `json:"process_samples"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &memory); err != nil || w.Code != 200 ||
		memory.Source != "cpp_backend_memory_snapshot" || memory.ModelTensorBytes == nil || *memory.ModelTensorBytes != 64 ||
		len(memory.ProcessSamples) != 1 || memory.ProcessSamples[0].Point != "backend_ready" || memory.ProcessSamples[0].ResidentBytes == nil {
		t.Fatalf("memory status=%d body=%s parsed=%+v err=%v", w.Code, w.Body, memory, err)
	}
}

func TestIntegrationNoModelAndValidation(t *testing.T) {
	f := startFixture(t, true)
	ctx := contextFor(t, 2*time.Second)
	state, err := f.client.Status(ctx)
	if err != nil || state.Response.State != pb.BackendState_BACKEND_STATE_NO_MODEL {
		t.Fatal(err)
	}
	result, err := f.client.Generate(ctx, request(1, "tokens"), nil)
	if status.Code(err) != codes.Unavailable || result.ErrorDetails == nil || result.ErrorDetails.Code != pb.ErrorCode_ERROR_CODE_BACKEND_UNAVAILABLE || result.TTFTMS != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	s, err := service.New(service.Config{Listen: "127.0.0.1:8080", BackendGRPC: f.target})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	stream, err := f.raw.Generate(ctx, &pb.GenerateRequest{RequestId: 0, MaxOutputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	withoutDeadline, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchdog := time.AfterFunc(time.Second, cancel)
	defer watchdog.Stop()
	stream, err = f.raw.Generate(withoutDeadline, request(2, "tokens"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing deadline: %v", err)
	}
	tooLong, cancelLong := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancelLong()
	stream, err = f.raw.Generate(tooLong, request(3, "tokens"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("excessive deadline: %v", err)
	}
}

func TestIntegrationQueueDuplicateAndQueuedCancel(t *testing.T) {
	f := startFixture(t, false)
	active := gate(t, f)
	ctx := contextFor(t, 2*time.Second)
	queued := open(t, f, 2, "tokens", ctx)
	full, err := f.client.Generate(ctx, request(3, "tokens"), nil)
	if status.Code(err) != codes.ResourceExhausted || full.ErrorDetails == nil || full.ErrorDetails.Code != pb.ErrorCode_ERROR_CODE_QUEUE_FULL {
		t.Fatalf("full=%+v err=%v", full, err)
	}
	duplicate, err := f.client.Generate(ctx, request(1, "tokens"), nil)
	if status.Code(err) != codes.AlreadyExists || duplicate.ErrorDetails == nil {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	for _, want := range []pb.CancelDisposition{pb.CancelDisposition_CANCEL_DISPOSITION_REQUESTED, pb.CancelDisposition_CANCEL_DISPOSITION_ALREADY_REQUESTED} {
		response, err := f.client.Cancel(ctx, 2)
		if err != nil || response.Disposition != want {
			t.Fatalf("cancel=%v err=%v", response, err)
		}
	}
	if _, err := f.client.Cancel(ctx, 999); status.Code(err) != codes.NotFound {
		t.Fatal(err)
	}
	if err := f.process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	if terminal(t, active).State != pb.TerminalState_TERMINAL_STATE_COMPLETED {
		t.Fatal("active request failed")
	}
	result := terminal(t, queued)
	if result.State != pb.TerminalState_TERMINAL_STATE_CANCELLED || result.Metrics.OutputTokens != 0 || result.Metrics.BackendExecutionMs != nil {
		t.Fatalf("queued cancel=%v", result)
	}
}

func TestIntegrationActiveCancel(t *testing.T) {
	f := startFixture(t, false)
	active := gate(t, f)
	if response, err := f.client.Cancel(contextFor(t, time.Second), 1); err != nil || response.Disposition != pb.CancelDisposition_CANCEL_DISPOSITION_REQUESTED {
		t.Fatal(err)
	}
	if err := f.process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	result := terminal(t, active)
	if result.State != pb.TerminalState_TERMINAL_STATE_CANCELLED || result.Metrics.OutputTokens != 1 {
		t.Fatalf("active cancel=%v", result)
	}
}

func TestIntegrationQueuedDeadlineAndRecovery(t *testing.T) {
	f := startFixture(t, false)
	active := gate(t, f)
	queued := open(t, f, 2, "tokens", contextFor(t, 80*time.Millisecond))
	if _, err := queued.Recv(); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("deadline: %v", err)
	}
	if err := f.process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	_ = terminal(t, active)
	result, err := f.client.Generate(contextFor(t, time.Second), request(3, "tokens"), nil)
	if err != nil || !result.TransportComplete {
		t.Fatalf("recovery: %+v %v", result, err)
	}
}

func TestIntegrationBlockedReaderDeadlineAndRecovery(t *testing.T) {
	f := startFixture(t, false)
	ctx := contextFor(t, 100*time.Millisecond)
	_, err := f.raw.Generate(ctx, &pb.GenerateRequest{RequestId: 1, Prompt: "flood", MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	<-ctx.Done() // Deliberately never read; transport flow control must remain bounded.
	result, err := f.client.Generate(contextFor(t, time.Second), request(2, "tokens"), nil)
	if err != nil || !result.TransportComplete {
		t.Fatalf("blocked reader recovery: %+v %v", result, err)
	}
}

func TestIntegrationFailureOversizeDisconnectAndEmpty(t *testing.T) {
	f := startFixture(t, false)
	for i, prompt := range []string{"failure", "oversize", "empty"} {
		result, err := f.client.Generate(contextFor(t, time.Second), request(uint64(i+1), prompt), nil)
		if err != nil || !result.TransportComplete {
			t.Fatalf("%s: %+v %v", prompt, result, err)
		}
		if prompt == "empty" {
			if result.Finished.State != pb.TerminalState_TERMINAL_STATE_COMPLETED || result.TTFTMS != nil || result.Finished.Metrics.TtftMs != nil {
				t.Fatal("empty stream timings fabricated")
			}
		} else if result.Finished.State != pb.TerminalState_TERMINAL_STATE_FAILED || strings.Contains(result.Finished.Error.Message, "secret") {
			t.Fatal("failure state/details invalid")
		}
	}
	want := errors.New("consumer disconnected")
	result, err := f.client.Generate(contextFor(t, time.Second), request(10, "flood"), func([]byte) error { return want })
	if !errors.Is(err, want) || result.TransportComplete || result.RPCCode != nil {
		t.Fatal("consumer failure mishandled")
	}
	result, err = f.client.Generate(contextFor(t, time.Second), request(11, "tokens"), nil)
	if err != nil || !result.TransportComplete {
		t.Fatalf("disconnect recovery: %+v %v", result, err)
	}
}

func TestIntegrationActiveDeadlineBeforeCleanup(t *testing.T) {
	f := startFixture(t, false)
	stream := open(t, f, 1, "gate", contextFor(t, 80*time.Millisecond))
	if event, err := stream.Recv(); err != nil || event.GetToken() == nil {
		t.Fatalf("active token: %v %v", event, err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("active deadline: %v", err)
	}
	// The fixture backend remains blocked until explicitly released. RPC timeout
	// therefore precedes backend cleanup, just as non-preemptible work can do.
	if err := f.process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	result, err := f.client.Generate(contextFor(t, time.Second), request(2, "tokens"), nil)
	if err != nil || !result.TransportComplete {
		t.Fatalf("post-cleanup recovery: %+v %v", result, err)
	}
}

func TestIntegrationShutdownWithActiveAndQueuedWork(t *testing.T) {
	f := startFixture(t, false)
	active := gate(t, f)
	queued := open(t, f, 2, "tokens", contextFor(t, 3*time.Second))
	if err := f.process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []grpc.ServerStreamingClient[pb.GenerateEvent]{active, queued} {
		for {
			_, err := stream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) && status.Code(err) != codes.Canceled && status.Code(err) != codes.Unavailable {
					t.Fatalf("shutdown stream: %v", err)
				}
				break
			}
		}
	}
	// startFixture's cleanup waits for exit status zero and joins its Wait goroutine.
}
