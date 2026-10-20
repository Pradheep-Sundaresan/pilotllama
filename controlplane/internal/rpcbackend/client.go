// Package rpcbackend implements the loopback-only internal inference client.
package rpcbackend

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	pb "inferno.local/controlplane/gen/inferno/v1"
)

const MessageLimit = 128 * 1024

func ValidateTarget(target string) error {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return errors.New("RPC target must be loopback IP:port")
	}
	ip := net.ParseIP(host)
	n, err := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || err != nil || n < 1 || n > 65535 {
		return errors.New("RPC target must be loopback IP:port with port 1..65535")
	}
	return nil
}

type Client struct {
	conn *grpc.ClientConn
	api  pb.InferenceServiceClient
}

func New(target string) (*Client, error) {
	if err := ValidateTarget(target); err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient("passthrough:///"+target,
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy(),
		grpc.WithDisableRetry(), grpc.WithDisableServiceConfig(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", target)
		}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MessageLimit), grpc.MaxCallSendMsgSize(MessageLimit)))
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, api: pb.NewInferenceServiceClient(conn)}, nil
}
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

type StatusObservation struct {
	Response  *pb.GetStatusResponse
	LatencyMS float64
}

func validateMemory(memory *pb.MemorySnapshot) bool {
	if memory == nil {
		return true
	}
	if len(memory.ProcessSamples) > 5 || len(memory.LlamaBuffers) > 32 ||
		memory.ConfiguredContextTokens < 32 || memory.ConfiguredBatchTokens == 0 ||
		memory.ConfiguredBatchTokens > memory.ConfiguredContextTokens ||
		memory.ConfiguredSequences == 0 || memory.ConfiguredSequences > 64 ||
		memory.ConfiguredGpuLayers < 0 || memory.ConfiguredGpuLayers > 1024 {
		return false
	}
	points := make(map[pb.MemorySamplePoint]bool, len(memory.ProcessSamples))
	for _, sample := range memory.ProcessSamples {
		if sample == nil || sample.Point < pb.MemorySamplePoint_MEMORY_SAMPLE_POINT_BACKEND_READY ||
			sample.Point > pb.MemorySamplePoint_MEMORY_SAMPLE_POINT_REQUEST_COMPLETE_AFTER_CLEANUP ||
			sample.Status < pb.MeasurementStatus_MEASUREMENT_STATUS_MEASURED ||
			sample.Status > pb.MeasurementStatus_MEASUREMENT_STATUS_UNAVAILABLE ||
			sample.MonotonicNs == 0 || points[sample.Point] {
			return false
		}
		points[sample.Point] = true
		if sample.Status == pb.MeasurementStatus_MEASUREMENT_STATUS_MEASURED {
			if sample.ResidentBytes == nil || sample.PeakResidentBytes == nil || sample.GetPeakResidentBytes() < sample.GetResidentBytes() {
				return false
			}
			if sample.PhysicalFootprintBytes != nil && sample.PeakPhysicalFootprintBytes != nil && sample.GetPeakPhysicalFootprintBytes() < sample.GetPhysicalFootprintBytes() {
				return false
			}
		} else if sample.ResidentBytes != nil || sample.PeakResidentBytes != nil || sample.PhysicalFootprintBytes != nil || sample.PeakPhysicalFootprintBytes != nil {
			return false
		}
	}
	const maxUint64 = ^uint64(0)
	var model, contextBytes, compute uint64
	names := make(map[string]bool, len(memory.LlamaBuffers))
	for _, row := range memory.LlamaBuffers {
		if row == nil || row.Name == "" || len(row.Name) > 128 || names[row.Name] ||
			maxUint64-model < row.ModelBytes || maxUint64-contextBytes < row.ContextBytes ||
			maxUint64-compute < row.ComputeBytes {
			return false
		}
		names[row.Name] = true
		model += row.ModelBytes
		contextBytes += row.ContextBytes
		compute += row.ComputeBytes
	}
	if len(memory.LlamaBuffers) == 0 {
		return memory.LlamaModelAllocatedBytes == nil && memory.LlamaContextAllocatedBytes == nil && memory.LlamaComputeAllocatedBytes == nil
	}
	return memory.LlamaModelAllocatedBytes != nil && memory.GetLlamaModelAllocatedBytes() == model &&
		memory.LlamaContextAllocatedBytes != nil && memory.GetLlamaContextAllocatedBytes() == contextBytes &&
		memory.LlamaComputeAllocatedBytes != nil && memory.GetLlamaComputeAllocatedBytes() == compute
}

func (c *Client) Status(ctx context.Context) (observation StatusObservation, err error) {
	start := time.Now()
	defer func() { observation.LatencyMS = float64(time.Since(start)) / float64(time.Millisecond) }()
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	response, err := c.api.GetStatus(ctx, &pb.GetStatusRequest{})
	if err != nil {
		return observation, err
	}
	if response == nil || response.State < pb.BackendState_BACKEND_STATE_READY || response.State > pb.BackendState_BACKEND_STATE_FAILED || len(response.GetModelId()) > 256 {
		return observation, status.Error(codes.DataLoss, "invalid backend status")
	}
	if scheduler := response.GetScheduler(); scheduler != nil &&
		(scheduler.QueueCapacity == 0 || scheduler.WaitingRequests > scheduler.QueueCapacity || scheduler.ExecutingRequests > 2) {
		return observation, status.Error(codes.DataLoss, "invalid scheduler snapshot")
	}
	if !validateMemory(response.GetMemory()) {
		return observation, status.Error(codes.DataLoss, "invalid memory snapshot")
	}
	observation.Response = response
	return observation, nil
}

func (c *Client) Cancel(ctx context.Context, id uint64) (*pb.CancelResponse, error) {
	if id == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid request ID")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	response, err := c.api.Cancel(ctx, &pb.CancelRequest{RequestId: id})
	if err != nil {
		return nil, err
	}
	if response == nil || response.RequestId != id || response.Disposition < pb.CancelDisposition_CANCEL_DISPOSITION_REQUESTED || response.Disposition > pb.CancelDisposition_CANCEL_DISPOSITION_FINISHED {
		return nil, status.Error(codes.DataLoss, "invalid cancellation response")
	}
	return response, nil
}

type Observation struct {
	RequestID              uint64
	LatencyMS              *float64
	TTFTMS                 *float64
	ReceivedTokens         uint32
	TokenBytes             uint64
	ReceiveTokensPerSecond *float64
	// Nil means no final transport status was observed (e.g. consumer failure).
	RPCCode           *codes.Code
	TransportComplete bool
	Finished          *pb.Finished
	ErrorDetails      *pb.InferenceError
}

func invalid(message string) error { return status.Error(codes.DataLoss, message) }
func validateFinished(f *pb.Finished, id uint64, count uint32) error {
	if f == nil || f.Metrics == nil || f.Metrics.RuntimeLatencyMs == nil || f.State < pb.TerminalState_TERMINAL_STATE_COMPLETED || f.State > pb.TerminalState_TERMINAL_STATE_FAILED {
		return invalid("invalid finished event")
	}
	m := f.Metrics
	for _, value := range []*float64{m.RuntimeLatencyMs, m.QueueWaitMs, m.BackendExecutionMs, m.PrefillPhaseMs, m.DecodePhaseMs, m.TtftMs, m.MeanInterTokenMs, m.DecodeTokensPerSecond} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0) {
			return invalid("invalid runtime metric")
		}
	}
	if m.OutputTokens < count || (f.State == pb.TerminalState_TERMINAL_STATE_COMPLETED && m.OutputTokens != count) {
		return invalid("inconsistent terminal token count")
	}
	if f.State == pb.TerminalState_TERMINAL_STATE_COMPLETED {
		if f.Error != nil {
			return invalid("successful result carries error")
		}
	} else {
		if !validDetail(f.Error, id) {
			return invalid("invalid terminal error")
		}
		if (f.State == pb.TerminalState_TERMINAL_STATE_CANCELLED && f.Error.Code != pb.ErrorCode_ERROR_CODE_CANCELLED) || (f.State == pb.TerminalState_TERMINAL_STATE_TIMED_OUT && f.Error.Code != pb.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED) {
			return invalid("terminal error disagrees with state")
		}
	}
	return nil
}

func validDetail(detail *pb.InferenceError, id uint64) bool {
	return detail != nil && detail.RequestId == id && detail.Code >= pb.ErrorCode_ERROR_CODE_INVALID_REQUEST && detail.Code <= pb.ErrorCode_ERROR_CODE_UNKNOWN_REQUEST_ID && len(detail.Message) <= 2048 && utf8.ValidString(detail.Message)
}

func trailerDetail(md metadata.MD, id uint64) (*pb.InferenceError, error) {
	values := md.Get("inferno-error-bin")
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) != 1 || len(values[0]) > 4096 {
		return nil, invalid("invalid error trailer size")
	}
	var detail pb.InferenceError
	if proto.Unmarshal([]byte(values[0]), &detail) != nil || !validDetail(&detail, id) {
		return nil, invalid("invalid error trailer")
	}
	return &detail, nil
}

// Generate calls consume synchronously for every native token (including empties).
// Consumers must return promptly; context cancellation cannot interrupt their code.
// A valid Finished with a failed native state is returned without an RPC error:
// callers must inspect Finished.State as well as TransportComplete.
func (c *Client) Generate(ctx context.Context, request *pb.GenerateRequest, consume func([]byte) error) (observation Observation, err error) {
	var start time.Time
	defer func() {
		if start.IsZero() {
			return
		}
		elapsed := float64(time.Since(start)) / float64(time.Millisecond)
		observation.LatencyMS = &elapsed
		if observation.ReceivedTokens > 0 && elapsed > 0 {
			rate := float64(observation.ReceivedTokens) / (elapsed / 1000)
			observation.ReceiveTokensPerSecond = &rate
		}
	}()
	if request == nil {
		return observation, status.Error(codes.InvalidArgument, "missing request")
	}
	observation.RequestID = request.RequestId
	if request.RequestId == 0 || request.MaxOutputTokens < 1 || request.MaxOutputTokens > 512 || len(request.Prompt) > 65536 || !utf8.ValidString(request.Prompt) || strings.Contains(request.Prompt, "<|im_start|>") || strings.Contains(request.Prompt, "<|im_end|>") || proto.Size(request) > MessageLimit {
		return observation, status.Error(codes.InvalidArgument, "invalid generation request")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > time.Hour {
		return observation, status.Error(codes.InvalidArgument, "require deadline at most one hour")
	}
	if ctx.Err() != nil {
		return observation, status.FromContextError(ctx.Err()).Err()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	start = time.Now()
	stream, err := c.api.Generate(ctx, request)
	if err != nil {
		code := status.Code(err)
		observation.RPCCode = &code
		return observation, err
	}
	accepted := false
	for {
		event, receiveErr := stream.Recv()
		if receiveErr != nil {
			code := status.Code(receiveErr)
			if errors.Is(receiveErr, io.EOF) {
				code = codes.OK
			}
			observation.RPCCode = &code
			if errors.Is(receiveErr, io.EOF) {
				if observation.Finished == nil {
					return observation, invalid("stream ended without finished event")
				}
				observation.TransportComplete = true
				return observation, nil
			}
			detail, detailErr := trailerDetail(stream.Trailer(), request.RequestId)
			if detailErr != nil {
				return observation, detailErr
			}
			observation.ErrorDetails = detail
			return observation, receiveErr
		}
		if event == nil || event.RequestId != request.RequestId || observation.Finished != nil {
			return observation, invalid("invalid stream ID or event ordering")
		}
		switch payload := event.Payload.(type) {
		case *pb.GenerateEvent_Accepted:
			if accepted || payload.Accepted == nil {
				return observation, invalid("duplicate/invalid accepted event")
			}
			accepted = true
		case *pb.GenerateEvent_Token:
			if !accepted || payload.Token == nil || payload.Token.OutputTokens != observation.ReceivedTokens+1 || payload.Token.OutputTokens > request.MaxOutputTokens || len(payload.Token.Piece) > 65536 {
				return observation, invalid("invalid token event")
			}
			if observation.TTFTMS == nil {
				value := float64(time.Since(start)) / float64(time.Millisecond)
				observation.TTFTMS = &value
			}
			observation.ReceivedTokens++
			observation.TokenBytes += uint64(len(payload.Token.Piece))
			if consume != nil {
				if err := consume(payload.Token.Piece); err != nil {
					return observation, err
				}
			}
		case *pb.GenerateEvent_Finished:
			if !accepted {
				return observation, invalid("finished before accepted")
			}
			if err := validateFinished(payload.Finished, request.RequestId, observation.ReceivedTokens); err != nil {
				return observation, err
			}
			if payload.Finished.Metrics.OutputTokens > request.MaxOutputTokens {
				return observation, invalid("terminal count exceeds budget")
			}
			observation.Finished = payload.Finished
		default:
			return observation, invalid("missing/unknown stream payload")
		}
	}
}
