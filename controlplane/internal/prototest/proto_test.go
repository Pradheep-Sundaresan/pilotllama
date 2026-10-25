package prototest

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	pb "inferno.local/controlplane/gen/inferno/v1"
)

func TestMetricPresence(t *testing.T) {
	source := &pb.RequestMetrics{RuntimeLatencyMs: proto.Float64(0), TtftMs: proto.Float64(0)}
	data, err := proto.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result pb.RequestMetrics
	if err := proto.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.RuntimeLatencyMs == nil || result.TtftMs == nil || result.GetTtftMs() != 0 {
		t.Fatal("present zero lost")
	}
	if result.QueueWaitMs != nil || result.DecodeTokensPerSecond != nil {
		t.Fatal("missing metric became present")
	}
}

func TestRawTokenAndID(t *testing.T) {
	for _, piece := range [][]byte{nil, {0xf0, 0x9f, 0}} {
		source := &pb.GenerateEvent{RequestId: math.MaxUint64, Payload: &pb.GenerateEvent_Token{Token: &pb.Token{Piece: piece, OutputTokens: 1, Channel: pb.TokenChannel_TOKEN_CHANNEL_REASONING, AdvancesOutputTokens: true}}}
		data, err := proto.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var result pb.GenerateEvent
		if err := proto.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.RequestId != math.MaxUint64 || result.GetToken() == nil || !bytes.Equal(result.GetToken().Piece, piece) || result.GetToken().OutputTokens != 1 || result.GetToken().Channel != pb.TokenChannel_TOKEN_CHANNEL_REASONING {
			t.Fatal("ID, token presence or raw bytes lost")
		}
	}
}

func TestStructuredChatRoundTrip(t *testing.T) {
	source := &pb.GenerateRequest{RequestId: 9, MaxOutputTokens: 32, EnableThinking: true, Messages: []*pb.ChatMessage{
		{Role: pb.ChatRole_CHAT_ROLE_SYSTEM, Content: "brief"},
		{Role: pb.ChatRole_CHAT_ROLE_USER, Content: "hello"},
	}}
	data, err := proto.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result pb.GenerateRequest
	if err := proto.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Prompt != "" || !result.EnableThinking || len(result.Messages) != 2 || result.Messages[0].Role != pb.ChatRole_CHAT_ROLE_SYSTEM || result.Messages[1].Content != "hello" {
		t.Fatal(&result)
	}
}

func TestMemoryPresence(t *testing.T) {
	zero := uint64(0)
	source := &pb.GetStatusResponse{State: pb.BackendState_BACKEND_STATE_READY, Memory: &pb.MemorySnapshot{
		ProcessSamples: []*pb.ProcessMemorySample{{Point: pb.MemorySamplePoint_MEMORY_SAMPLE_POINT_BACKEND_READY, Status: pb.MeasurementStatus_MEASUREMENT_STATUS_MEASURED, MonotonicNs: 1, ResidentBytes: &zero}},
		LlamaBuffers:   []*pb.LlamaBufferAllocation{{Name: "fixture", ContextBytes: 20}},
	}}
	data, err := proto.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result pb.GetStatusResponse
	if err := proto.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Memory == nil || len(result.Memory.ProcessSamples) != 1 || result.Memory.ProcessSamples[0].ResidentBytes == nil || result.Memory.ProcessSamples[0].GetResidentBytes() != 0 || result.Memory.ModelTensorBytes != nil || len(result.Memory.LlamaBuffers) != 1 {
		t.Fatal("memory presence or rows lost")
	}
}

func TestModelCapabilitiesRoundTrip(t *testing.T) {
	source := &pb.GetStatusResponse{ModelCapabilities: &pb.ModelCapabilities{
		Architecture: "gemma4", ParameterCount: 8_000_000_000, TrainedContextTokens: 131_072,
		ConfiguredContextTokens: 8192, ConfiguredMaxOutputTokens: 1024,
		HasDecoder: true, HasEmbeddedChatTemplate: true,
		Compatibility:  pb.CompatibilityLevel_COMPATIBILITY_LEVEL_COMPATIBLE,
		TemplateSource: pb.PromptTemplateSource_PROMPT_TEMPLATE_SOURCE_EMBEDDED,
		QueueMode:      pb.CapabilitySupport_CAPABILITY_SUPPORT_DETECTED,
		ContinuousMode: pb.CapabilitySupport_CAPABILITY_SUPPORT_UNQUALIFIED,
	}}
	data, err := proto.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result pb.GetStatusResponse
	if err := proto.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.ModelCapabilities == nil || result.ModelCapabilities.Architecture != "gemma4" ||
		result.ModelCapabilities.ParameterCount != 8_000_000_000 ||
		result.ModelCapabilities.ConfiguredContextTokens != 8192 ||
		result.ModelCapabilities.ConfiguredMaxOutputTokens != 1024 ||
		result.ModelCapabilities.ContinuousMode != pb.CapabilitySupport_CAPABILITY_SUPPORT_UNQUALIFIED {
		t.Fatal("model capabilities lost on wire")
	}
}

func TestServiceShape(t *testing.T) {
	service := pb.File_inferno_v1_inference_proto.Services().ByName("InferenceService")
	if service == nil || service.Methods().Len() != 3 {
		t.Fatal("service shape changed")
	}
	generate := service.Methods().ByName("Generate")
	if generate == nil || !generate.IsStreamingServer() || generate.IsStreamingClient() {
		t.Fatal("Generate must be server-streaming")
	}
	for _, name := range []string{"GetStatus", "Cancel"} {
		method := service.Methods().ByName(protoreflect.Name(name))
		if method == nil || method.IsStreamingServer() || method.IsStreamingClient() {
			t.Fatalf("%s must be unary", name)
		}
	}
}

func TestStructuredFailure(t *testing.T) {
	source := &pb.GenerateEvent{RequestId: 42, Payload: &pb.GenerateEvent_Accepted{Accepted: &pb.Accepted{}}}
	source.Payload = &pb.GenerateEvent_Finished{Finished: &pb.Finished{
		State: pb.TerminalState_TERMINAL_STATE_FAILED, Metrics: &pb.RequestMetrics{OutputTokens: 1},
		Error: &pb.InferenceError{RequestId: 42, Code: pb.ErrorCode_ERROR_CODE_BACKEND_FAILURE, Message: "backend failure"},
	}}
	data, err := proto.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result pb.GenerateEvent
	if err := proto.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(source, &result) || result.GetAccepted() != nil || result.GetFinished().GetError().GetCode() != pb.ErrorCode_ERROR_CODE_BACKEND_FAILURE {
		t.Fatal("structured failure or oneof lost")
	}
	var details pb.InferenceError
	trailer, err := proto.Marshal(result.GetFinished().Error)
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(trailer, &details); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(&details, source.GetFinished().Error) {
		t.Fatal("binary error trailer roundtrip failed")
	}
}

func TestCrossLanguageWire(t *testing.T) {
	directory := os.Getenv("INFERNO_PROTO_INTEROP_DIR")
	if directory == "" {
		t.Skip("Run scripts/test-proto-interop.sh")
	}
	read := func(name string) *pb.GenerateEvent {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		var event pb.GenerateEvent
		if err := proto.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		return &event
	}
	write := func(name string, event *pb.GenerateEvent) {
		t.Helper()
		data, err := proto.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	token := read("cpp-token.bin")
	if token.RequestId != math.MaxUint64 || token.GetToken() == nil || token.GetToken().OutputTokens != 1 || !bytes.Equal(token.GetToken().Piece, []byte{0xf0, 0x9f, 0}) {
		t.Fatal("C++ token fields lost")
	}
	finished := read("cpp-finished.bin")
	result := finished.GetFinished()
	if finished.RequestId != token.RequestId || result == nil || result.State != pb.TerminalState_TERMINAL_STATE_FAILED {
		t.Fatal("C++ terminal fields lost")
	}
	m := result.GetMetrics()
	if m == nil || m.OutputTokens != 1 || m.RuntimeLatencyMs == nil || m.GetRuntimeLatencyMs() != 5 || m.TtftMs == nil || m.GetTtftMs() != 0 || m.QueueWaitMs != nil {
		t.Fatal("C++ metric presence lost")
	}
	if result.Error == nil || result.Error.RequestId != token.RequestId || result.Error.Code != pb.ErrorCode_ERROR_CODE_BACKEND_FAILURE || result.Error.Message != "fixture failure" {
		t.Fatal("C++ structured error lost")
	}
	token.GetToken().Piece = []byte{0x98, 0x80, 0}
	token.GetToken().OutputTokens = 2
	write("go-token.bin", token)
	result.State = pb.TerminalState_TERMINAL_STATE_TIMED_OUT
	result.Metrics.TtftMs = nil
	result.Metrics.QueueWaitMs = proto.Float64(0)
	result.Error.Code = pb.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED
	write("go-finished.bin", finished)
}
