package service

import (
	"encoding/json"
	"testing"

	pb "inferno.local/controlplane/gen/inferno/v1"
)

func u64(value uint64) *uint64 { return &value }

func TestMemoryEndpointReportsAuthenticatedSnapshot(t *testing.T) {
	s, backend := authenticatedService(t, testSecret)
	backend.memory = &pb.MemorySnapshot{
		ProcessSamples: []*pb.ProcessMemorySample{{Point: pb.MemorySamplePoint_MEMORY_SAMPLE_POINT_BACKEND_READY,
			Status: pb.MeasurementStatus_MEASUREMENT_STATUS_MEASURED, MonotonicNs: 10,
			ResidentBytes: u64(100), PeakResidentBytes: u64(120), PhysicalFootprintBytes: u64(80), PeakPhysicalFootprintBytes: u64(90)}},
		ModelTensorBytes: u64(64), LlamaBuffers: []*pb.LlamaBufferAllocation{{Name: "fixture", ModelBytes: 60, ContextBytes: 20, ComputeBytes: 10}},
		LlamaModelAllocatedBytes: u64(60), LlamaContextAllocatedBytes: u64(20), LlamaComputeAllocatedBytes: u64(10),
		ConfiguredContextTokens: 4096, ConfiguredBatchTokens: 128, ConfiguredSequences: 1,
	}
	w := controlRequest(s, "GET", "/internal/v1/memory", testSecret)
	var report memoryReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || report.Source != "cpp_backend_memory_snapshot" || report.Unit != "bytes" ||
		len(report.ProcessSamples) != 1 || report.ProcessSamples[0].Point != "backend_ready" ||
		report.ProcessSamples[0].ResidentBytes == nil || *report.ProcessSamples[0].ResidentBytes != 100 ||
		len(report.LlamaBuffers) != 1 || report.Configuration.ContextTokens != 4096 ||
		report.ObservedAtUnixMS == 0 || backend.statusCalls != 1 || backend.generationCalls != 0 {
		t.Fatalf("status=%d report=%+v backend=%+v", w.Code, report, backend)
	}
	if w := controlRequest(s, "GET", "/internal/v1/memory", ""); w.Code != 401 {
		t.Fatal("memory auth bypassed")
	}
}

func TestMemoryEndpointRejectsUnavailableSnapshot(t *testing.T) {
	s, _ := authenticatedService(t, testSecret)
	if w := controlRequest(s, "GET", "/internal/v1/memory", testSecret); w.Code != 502 {
		t.Fatalf("missing memory status=%d body=%s", w.Code, w.Body)
	}
	s.authDigest = nil
	if w := controlRequest(s, "GET", "/internal/v1/memory", testSecret); w.Code != 403 {
		t.Fatal("memory available without configured control auth")
	}
}
