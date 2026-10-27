package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	pb "inferno.local/controlplane/gen/inferno/v1"
)

func TestModelCapabilitiesReportsLoadedFactsAndQualificationBoundaries(t *testing.T) {
	s, backend := authenticatedService(t, testSecret)
	s.modelConfigDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(s.modelConfigDir, "driving-test.json"), []byte(`{"observed_at_unix_ms":1,"model_id":"test-model","file_name":"model.gguf","grade":"verified","checks":{"basic_generation":true},"continuous_qualified":true,"speculative_decoding":{"draft_file_name":"draft.gguf","draft_file_size_bytes":42,"drafted_tokens":10,"accepted_tokens":4,"acceptance_rate":0.4},"method":"bounded_local_driving_test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	backend.capabilities = &pb.ModelCapabilities{
		Architecture: "gemma4", ParameterCount: 8_000_000_000, TrainedContextTokens: 131_072,
		ConfiguredContextTokens: 8192, ConfiguredMaxOutputTokens: 1024,
		HasDecoder: true, HasEmbeddedChatTemplate: true,
		Compatibility:  pb.CompatibilityLevel_COMPATIBILITY_LEVEL_COMPATIBLE,
		TemplateSource: pb.PromptTemplateSource_PROMPT_TEMPLATE_SOURCE_EMBEDDED,
		QueueMode:      pb.CapabilitySupport_CAPABILITY_SUPPORT_DETECTED,
		ContinuousMode: pb.CapabilitySupport_CAPABILITY_SUPPORT_UNQUALIFIED,
		SystemMessages: pb.CapabilitySupport_CAPABILITY_SUPPORT_DETECTED,
		MultiTurn:      pb.CapabilitySupport_CAPABILITY_SUPPORT_DETECTED,
		Thinking:       pb.CapabilitySupport_CAPABILITY_SUPPORT_DETECTED,
		Tools:          pb.CapabilitySupport_CAPABILITY_SUPPORT_UNAVAILABLE,
	}
	w := controlRequest(s, "GET", "/internal/v1/model-capabilities", testSecret)
	var report modelCapabilitiesReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || report.Source != "cpp_loaded_gguf_capability_probe" || report.Architecture != "gemma4" ||
		report.ParameterCount != 8_000_000_000 || report.TrainedContextTokens != 131_072 ||
		report.ConfiguredContextTokens != 8192 || report.ConfiguredMaxOutputTokens != 1024 ||
		report.Compatibility != "compatible" || report.TemplateSource != "embedded" ||
		report.Capabilities["queue_mode"] != "detected" || report.Capabilities["continuous_mode"] != "unqualified" ||
		report.Capabilities["thinking"] != "detected" || report.Capabilities["tools"] != "unavailable" || report.DrivingTest == nil || report.DrivingTest.Grade != "verified" ||
		report.DrivingTest.SpeculativeDecoding == nil || report.DrivingTest.SpeculativeDecoding.AcceptedTokens != 4 {
		t.Fatalf("status=%d report=%+v", w.Code, report)
	}
	if w := controlRequest(s, "GET", "/internal/v1/model-capabilities", ""); w.Code != 401 {
		t.Fatal("model capability auth bypassed")
	}
}

func TestModelCapabilitiesRejectsMissingOrInvalidNativeReport(t *testing.T) {
	s, backend := authenticatedService(t, testSecret)
	if w := controlRequest(s, "GET", "/internal/v1/model-capabilities", testSecret); w.Code != 502 {
		t.Fatalf("missing capability status=%d body=%s", w.Code, w.Body)
	}
	backend.capabilities = &pb.ModelCapabilities{Architecture: "bad", HasDecoder: true}
	if w := controlRequest(s, "GET", "/internal/v1/model-capabilities", testSecret); w.Code != 502 {
		t.Fatalf("invalid capability status=%d body=%s", w.Code, w.Body)
	}
}
