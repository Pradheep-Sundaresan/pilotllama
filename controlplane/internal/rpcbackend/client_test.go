package rpcbackend

import (
	"context"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	pb "inferno.local/controlplane/gen/inferno/v1"
)

type fakeStream struct {
	grpc.ClientStream
	events  []*pb.GenerateEvent
	err     error
	trailer metadata.MD
}

func (s *fakeStream) Recv() (*pb.GenerateEvent, error) {
	if len(s.events) == 0 {
		if s.err != nil {
			return nil, s.err
		}
		return nil, io.EOF
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event, nil
}
func (s *fakeStream) Trailer() metadata.MD { return s.trailer }

type fakeAPI struct {
	stream *fakeStream
	ctx    context.Context
	status *pb.GetStatusResponse
	cancel *pb.CancelResponse
}

func (f *fakeAPI) Generate(ctx context.Context, _ *pb.GenerateRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pb.GenerateEvent], error) {
	f.ctx = ctx
	return f.stream, nil
}
func (f *fakeAPI) GetStatus(context.Context, *pb.GetStatusRequest, ...grpc.CallOption) (*pb.GetStatusResponse, error) {
	return f.status, nil
}
func (f *fakeAPI) Cancel(context.Context, *pb.CancelRequest, ...grpc.CallOption) (*pb.CancelResponse, error) {
	return f.cancel, nil
}

func accepted() *pb.GenerateEvent {
	return &pb.GenerateEvent{RequestId: 42, Payload: &pb.GenerateEvent_Accepted{Accepted: &pb.Accepted{}}}
}
func token(n uint32, piece []byte) *pb.GenerateEvent {
	return &pb.GenerateEvent{RequestId: 42, Payload: &pb.GenerateEvent_Token{Token: &pb.Token{OutputTokens: n, Piece: piece, Channel: pb.TokenChannel_TOKEN_CHANNEL_CONTENT, AdvancesOutputTokens: true}}}
}
func finished(n uint32) *pb.GenerateEvent {
	return &pb.GenerateEvent{RequestId: 42, Payload: &pb.GenerateEvent_Finished{Finished: &pb.Finished{State: pb.TerminalState_TERMINAL_STATE_COMPLETED, Metrics: &pb.RequestMetrics{OutputTokens: n, RuntimeLatencyMs: proto.Float64(1)}}}}
}
func request() *pb.GenerateRequest {
	return &pb.GenerateRequest{RequestId: 42, Prompt: "tokens", MaxOutputTokens: 8}
}

func TestProtocolValidation(t *testing.T) {
	badMetric := finished(0)
	badMetric.GetFinished().Metrics.RuntimeLatencyMs = proto.Float64(math.NaN())
	wrongID := accepted()
	wrongID.RequestId = 43
	for _, tc := range []struct {
		name   string
		events []*pb.GenerateEvent
		valid  bool
	}{
		{"zero-output", []*pb.GenerateEvent{accepted(), finished(0)}, true},
		{"raw-and-empty", []*pb.GenerateEvent{accepted(), token(1, []byte{0xf0, 0x9f}), token(2, nil), finished(2)}, true},
		{"wrong-ID", []*pb.GenerateEvent{wrongID}, false},
		{"no-accepted", []*pb.GenerateEvent{token(1, nil)}, false},
		{"duplicate-accepted", []*pb.GenerateEvent{accepted(), accepted()}, false},
		{"wrong-count", []*pb.GenerateEvent{accepted(), token(2, nil)}, false},
		{"missing-finished", []*pb.GenerateEvent{accepted()}, false},
		{"early-finished", []*pb.GenerateEvent{finished(0)}, false},
		{"extra-event", []*pb.GenerateEvent{accepted(), finished(0), token(1, nil)}, false},
		{"bad-metric", []*pb.GenerateEvent{accepted(), badMetric}, false},
		{"missing-payload", []*pb.GenerateEvent{{RequestId: 42}}, false},
		{"count-mismatch", []*pb.GenerateEvent{accepted(), finished(1)}, false},
		{"missing-channel", []*pb.GenerateEvent{accepted(), &pb.GenerateEvent{RequestId: 42, Payload: &pb.GenerateEvent_Token{Token: &pb.Token{OutputTokens: 1}}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			api := &fakeAPI{stream: &fakeStream{events: tc.events}}
			client := &Client{api: api}
			result, err := client.Generate(ctx, request(), nil)
			if (err == nil) != tc.valid || result.TransportComplete != tc.valid {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if !tc.valid && status.Code(err) != codes.DataLoss {
				t.Fatalf("wrong error: %v", err)
			}
			if result.LatencyMS == nil {
				t.Fatal("RPC duration unavailable after opening call")
			}
			if result.ReceivedTokens == 0 && (result.TTFTMS != nil || result.ReceiveTokensPerSecond != nil) {
				t.Fatal("fabricated token timing")
			}
			if api.ctx.Err() == nil {
				t.Fatal("outgoing context was not released")
			}
		})
	}
}

func TestChannelAwareReasoningStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reasoning := token(1, []byte("why"))
	reasoning.GetToken().Channel = pb.TokenChannel_TOKEN_CHANNEL_REASONING
	flush := token(1, []byte(" later"))
	flush.GetToken().Channel = pb.TokenChannel_TOKEN_CHANNEL_REASONING
	flush.GetToken().AdvancesOutputTokens = false
	api := &fakeAPI{stream: &fakeStream{events: []*pb.GenerateEvent{accepted(), reasoning, flush, token(2, []byte("answer")), finished(2)}}}
	request := request()
	request.EnableThinking = true
	var pieces []OutputPiece
	result, err := (&Client{api: api}).GeneratePieces(ctx, request, func(piece OutputPiece) error {
		pieces = append(pieces, OutputPiece{Bytes: append([]byte(nil), piece.Bytes...), Channel: piece.Channel, AdvancesOutputTokens: piece.AdvancesOutputTokens})
		return nil
	})
	if err != nil || !result.TransportComplete || result.ReceivedTokens != 2 || len(pieces) != 3 || pieces[0].Channel != pb.TokenChannel_TOKEN_CHANNEL_REASONING || string(pieces[0].Bytes) != "why" || pieces[1].AdvancesOutputTokens || string(pieces[1].Bytes) != " later" || pieces[2].Channel != pb.TokenChannel_TOKEN_CHANNEL_CONTENT || string(pieces[2].Bytes) != "answer" {
		t.Fatalf("result=%+v pieces=%+v err=%v", result, pieces, err)
	}
	api = &fakeAPI{}
	if _, err := (&Client{api: api}).Generate(ctx, request, nil); status.Code(err) != codes.InvalidArgument || api.ctx != nil {
		t.Fatal("legacy byte consumer accepted a thinking request")
	}
}

func TestPartialStreamAndErrorTrailer(t *testing.T) {
	detail := &pb.InferenceError{RequestId: 42, Code: pb.ErrorCode_ERROR_CODE_QUEUE_FULL, Message: "queue full"}
	data, _ := proto.Marshal(detail)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &fakeAPI{stream: &fakeStream{events: []*pb.GenerateEvent{accepted(), token(1, []byte("x"))}, err: status.Error(codes.ResourceExhausted, "queue full"), trailer: metadata.Pairs("inferno-error-bin", string(data))}}
	result, err := (&Client{api: api}).Generate(ctx, request(), nil)
	if status.Code(err) != codes.ResourceExhausted || result.TransportComplete || result.ReceivedTokens != 1 || result.TokenBytes != 1 || result.TTFTMS == nil || result.Finished != nil || !proto.Equal(result.ErrorDetails, detail) {
		t.Fatalf("partial result=%+v err=%v", result, err)
	}
	api.stream = &fakeStream{err: status.Error(codes.Internal, "bad"), trailer: metadata.Pairs("inferno-error-bin", "invalid")}
	if _, err := (&Client{api: api}).Generate(ctx, request(), nil); status.Code(err) != codes.DataLoss {
		t.Fatal("malformed trailer accepted")
	}
}

func TestConsumerFailureCancelsWithoutInventedStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	api := &fakeAPI{stream: &fakeStream{events: []*pb.GenerateEvent{accepted(), token(1, nil), finished(1)}}}
	want := errors.New("consumer failed")
	result, err := (&Client{api: api}).Generate(ctx, request(), func([]byte) error { return want })
	if !errors.Is(err, want) || api.ctx.Err() == nil || result.RPCCode != nil || result.TransportComplete || result.ReceivedTokens != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestValidationWithoutOpeningRPC(t *testing.T) {
	for _, target := range []string{"localhost:8081", "0.0.0.0:8081", "192.0.2.1:8081", "127.0.0.1:0", "127.0.0.1:65536", "http://127.0.0.1:8081"} {
		if ValidateTarget(target) == nil {
			t.Fatalf("unsafe target %q", target)
		}
	}
	for _, target := range []string{"127.0.0.1:8081", "[::1]:8081"} {
		client, err := New(target)
		if err != nil {
			t.Fatal(err)
		}
		_ = client.Close()
	}
	client := &Client{}
	result, err := client.Generate(context.Background(), request(), nil)
	if status.Code(err) != codes.InvalidArgument || result.LatencyMS != nil {
		t.Fatal("missing deadline accepted or fabricated RPC latency")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := client.Generate(ctx, request(), nil); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}
}

func TestStatusAndCancelResponseValidation(t *testing.T) {
	resident, peak := uint64(100), uint64(120)
	model, contextBytes, compute := uint64(60), uint64(20), uint64(10)
	api := &fakeAPI{status: &pb.GetStatusResponse{State: pb.BackendState_BACKEND_STATE_READY,
		Scheduler: &pb.SchedulerSnapshot{QueueCapacity: 2, WaitingRequests: 1, ExecutingRequests: 1},
		Memory: &pb.MemorySnapshot{ProcessSamples: []*pb.ProcessMemorySample{{Point: pb.MemorySamplePoint_MEMORY_SAMPLE_POINT_BACKEND_READY,
			Status: pb.MeasurementStatus_MEASUREMENT_STATUS_MEASURED, MonotonicNs: 1, ResidentBytes: &resident, PeakResidentBytes: &peak}},
			LlamaBuffers:             []*pb.LlamaBufferAllocation{{Name: "fixture", ModelBytes: model, ContextBytes: contextBytes, ComputeBytes: compute}},
			LlamaModelAllocatedBytes: &model, LlamaContextAllocatedBytes: &contextBytes, LlamaComputeAllocatedBytes: &compute,
			ConfiguredContextTokens: 4096, ConfiguredBatchTokens: 128, ConfiguredSequences: 1}},
		cancel: &pb.CancelResponse{RequestId: 42, Disposition: pb.CancelDisposition_CANCEL_DISPOSITION_REQUESTED}}
	client := &Client{api: api}
	if result, err := client.Status(context.Background()); err != nil || result.Response.State != pb.BackendState_BACKEND_STATE_READY {
		t.Fatal(err)
	}
	api.status.Scheduler.ExecutingRequests = 2
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatal("two active batch sequences rejected", err)
	}
	api.status.Scheduler.ExecutingRequests = 3
	if _, err := client.Status(context.Background()); status.Code(err) != codes.DataLoss {
		t.Fatal("invalid active sequence count accepted")
	}
	api.status.Scheduler.ExecutingRequests = 1
	api.status.Scheduler.WaitingRequests = 3
	if _, err := client.Status(context.Background()); status.Code(err) != codes.DataLoss {
		t.Fatal("invalid scheduler snapshot accepted")
	}
	api.status.Scheduler.WaitingRequests = 1
	badPeak := uint64(99)
	api.status.Memory.ProcessSamples[0].PeakResidentBytes = &badPeak
	if _, err := client.Status(context.Background()); status.Code(err) != codes.DataLoss {
		t.Fatal("invalid memory snapshot accepted")
	}
	api.status.Memory.ProcessSamples[0].PeakResidentBytes = &peak
	api.status.State = pb.BackendState_BACKEND_STATE_UNSPECIFIED
	if _, err := client.Status(context.Background()); status.Code(err) != codes.DataLoss {
		t.Fatal("invalid status accepted")
	}
	if _, err := client.Cancel(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	api.cancel.RequestId = 43
	if _, err := client.Cancel(context.Background(), 42); status.Code(err) != codes.DataLoss {
		t.Fatal("wrong cancellation ID accepted")
	}
}
