package service

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "inferno.local/controlplane/gen/inferno/v1"
)

type modelCapabilitiesReport struct {
	ObservedAtUnixMS          int64               `json:"observed_at_unix_ms"`
	Source                    string              `json:"source"`
	BackendState              string              `json:"backend_state"`
	StatusRPCLatencyMS        float64             `json:"status_rpc_latency_ms"`
	Architecture              string              `json:"architecture"`
	ParameterCount            uint64              `json:"parameter_count"`
	TrainedContextTokens      uint32              `json:"trained_context_tokens"`
	ConfiguredContextTokens   uint32              `json:"configured_context_tokens"`
	ConfiguredMaxOutputTokens uint32              `json:"configured_max_output_tokens"`
	HasEncoder                bool                `json:"has_encoder"`
	HasDecoder                bool                `json:"has_decoder"`
	HasEmbeddedChatTemplate   bool                `json:"has_embedded_chat_template"`
	Compatibility             string              `json:"compatibility"`
	TemplateSource            string              `json:"template_source"`
	Capabilities              map[string]string   `json:"capabilities"`
	DrivingTest               *drivingTestSummary `json:"driving_test,omitempty"`
}

type drivingTestSummary struct {
	ObservedAtUnixMS    int64           `json:"observed_at_unix_ms"`
	ModelID             string          `json:"model_id"`
	FileName            string          `json:"file_name"`
	Grade               string          `json:"grade"`
	Checks              map[string]bool `json:"checks"`
	ContinuousQualified bool            `json:"continuous_qualified"`
	PhysicalMemoryBytes *uint64         `json:"physical_memory_bytes,omitempty"`
	Method              string          `json:"method"`
}

func (s *Service) drivingTest(modelID string) *drivingTestSummary {
	if s.modelConfigDir == "" {
		return nil
	}
	path := filepath.Join(s.modelConfigDir, "driving-test.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1024*1024 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var report drivingTestSummary
	if json.Unmarshal(data, &report) != nil || report.ModelID != modelID || report.ObservedAtUnixMS <= 0 || report.FileName == "" || report.Method != "bounded_local_driving_test" ||
		(report.Grade != "verified" && report.Grade != "compatible" && report.Grade != "experimental" && report.Grade != "unsupported") {
		return nil
	}
	return &report
}

func capabilitySupportName(value pb.CapabilitySupport) string {
	switch value {
	case pb.CapabilitySupport_CAPABILITY_SUPPORT_UNAVAILABLE:
		return "unavailable"
	case pb.CapabilitySupport_CAPABILITY_SUPPORT_DETECTED:
		return "detected"
	case pb.CapabilitySupport_CAPABILITY_SUPPORT_VERIFIED:
		return "verified"
	case pb.CapabilitySupport_CAPABILITY_SUPPORT_UNQUALIFIED:
		return "unqualified"
	default:
		return "unknown"
	}
}

func (s *Service) modelCapabilities(w http.ResponseWriter, r *http.Request) {
	observation, err := s.rpc.Status(r.Context())
	if err != nil {
		code, body := mappedError(err)
		writeJSON(w, code, body)
		return
	}
	if observation.Response == nil || observation.Response.ModelCapabilities == nil {
		code, body := mappedError(status.Error(codes.DataLoss, "missing model capability report"))
		writeJSON(w, code, body)
		return
	}
	c := observation.Response.ModelCapabilities
	if c.Architecture == "" || len(c.Architecture) > 128 || c.ParameterCount == 0 || c.TrainedContextTokens == 0 ||
		c.ConfiguredContextTokens < 32 || c.ConfiguredContextTokens > c.TrainedContextTokens ||
		c.ConfiguredMaxOutputTokens == 0 || c.ConfiguredMaxOutputTokens > c.ConfiguredContextTokens ||
		!c.HasDecoder || c.Compatibility == pb.CompatibilityLevel_COMPATIBILITY_LEVEL_UNSPECIFIED ||
		c.TemplateSource == pb.PromptTemplateSource_PROMPT_TEMPLATE_SOURCE_UNSPECIFIED {
		code, body := mappedError(status.Error(codes.DataLoss, "invalid model capability report"))
		writeJSON(w, code, body)
		return
	}
	compatibility := "compatible"
	if c.Compatibility == pb.CompatibilityLevel_COMPATIBILITY_LEVEL_VERIFIED {
		compatibility = "verified"
	}
	templateSource := "embedded"
	if c.TemplateSource == pb.PromptTemplateSource_PROMPT_TEMPLATE_SOURCE_VERIFIED_FALLBACK {
		templateSource = "verified_fallback"
	}
	report := modelCapabilitiesReport{
		ObservedAtUnixMS: time.Now().UnixMilli(), Source: "cpp_loaded_gguf_capability_probe",
		BackendState: backendStateName(observation.Response.State), StatusRPCLatencyMS: observation.LatencyMS,
		Architecture: c.Architecture, ParameterCount: c.ParameterCount,
		TrainedContextTokens: c.TrainedContextTokens, ConfiguredContextTokens: c.ConfiguredContextTokens,
		ConfiguredMaxOutputTokens: c.ConfiguredMaxOutputTokens, HasEncoder: c.HasEncoder, HasDecoder: c.HasDecoder,
		HasEmbeddedChatTemplate: c.HasEmbeddedChatTemplate, Compatibility: compatibility, TemplateSource: templateSource,
		Capabilities: map[string]string{
			"queue_mode": capabilitySupportName(c.QueueMode), "continuous_mode": capabilitySupportName(c.ContinuousMode),
			"system_messages": capabilitySupportName(c.SystemMessages), "multi_turn": capabilitySupportName(c.MultiTurn),
			"thinking": capabilitySupportName(c.Thinking), "tools": capabilitySupportName(c.Tools),
		},
	}
	report.DrivingTest = s.drivingTest(observation.Response.GetModelId())
	writeJSON(w, http.StatusOK, report)
}
