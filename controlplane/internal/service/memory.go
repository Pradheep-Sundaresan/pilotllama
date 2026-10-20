package service

import (
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "inferno.local/controlplane/gen/inferno/v1"
)

type processMemoryReport struct {
	Point                      string  `json:"point"`
	Status                     string  `json:"status"`
	MonotonicNS                uint64  `json:"monotonic_ns"`
	ResidentBytes              *uint64 `json:"resident_bytes"`
	PeakResidentBytes          *uint64 `json:"peak_resident_bytes"`
	PhysicalFootprintBytes     *uint64 `json:"physical_footprint_bytes"`
	PeakPhysicalFootprintBytes *uint64 `json:"peak_physical_footprint_bytes"`
}

type llamaBufferReport struct {
	Name         string `json:"name"`
	ModelBytes   uint64 `json:"model_bytes"`
	ContextBytes uint64 `json:"context_bytes"`
	ComputeBytes uint64 `json:"compute_bytes"`
}

type memoryConfiguration struct {
	ContextTokens uint32 `json:"context_tokens"`
	BatchTokens   uint32 `json:"batch_tokens"`
	Sequences     uint32 `json:"sequences"`
	GPULayers     int32  `json:"gpu_layers"`
}

type memoryReport struct {
	ObservedAtUnixMS           int64                 `json:"observed_at_unix_ms"`
	Source                     string                `json:"source"`
	Unit                       string                `json:"unit"`
	BackendState               string                `json:"backend_state"`
	StatusRPCLatencyMS         float64               `json:"status_rpc_latency_ms"`
	ProcessSamples             []processMemoryReport `json:"process_samples"`
	ModelTensorBytes           *uint64               `json:"model_tensor_bytes"`
	LlamaBuffers               []llamaBufferReport   `json:"llama_buffers"`
	LlamaModelAllocatedBytes   *uint64               `json:"llama_model_allocated_bytes"`
	LlamaContextAllocatedBytes *uint64               `json:"llama_context_allocated_bytes"`
	LlamaComputeAllocatedBytes *uint64               `json:"llama_compute_allocated_bytes"`
	Configuration              memoryConfiguration   `json:"configuration"`
}

func memoryPointName(point pb.MemorySamplePoint) string {
	switch point {
	case pb.MemorySamplePoint_MEMORY_SAMPLE_POINT_BACKEND_READY:
		return "backend_ready"
	case pb.MemorySamplePoint_MEMORY_SAMPLE_POINT_REQUEST_START:
		return "request_start"
	case pb.MemorySamplePoint_MEMORY_SAMPLE_POINT_PREFILL_COMPLETE:
		return "prefill_complete"
	case pb.MemorySamplePoint_MEMORY_SAMPLE_POINT_REQUEST_COMPLETE_BEFORE_CLEANUP:
		return "request_complete_before_cleanup"
	case pb.MemorySamplePoint_MEMORY_SAMPLE_POINT_REQUEST_COMPLETE_AFTER_CLEANUP:
		return "request_complete_after_cleanup"
	default:
		return "unknown"
	}
}

func measurementStatusName(value pb.MeasurementStatus) string {
	if value == pb.MeasurementStatus_MEASUREMENT_STATUS_MEASURED {
		return "measured"
	}
	return "unavailable"
}

func (s *Service) memoryStatus(w http.ResponseWriter, r *http.Request) {
	observation, err := s.rpc.Status(r.Context())
	if err != nil {
		code, body := mappedError(err)
		writeJSON(w, code, body)
		return
	}
	if observation.Response == nil || observation.Response.Memory == nil {
		code, body := mappedError(status.Error(codes.DataLoss, "missing memory snapshot"))
		writeJSON(w, code, body)
		return
	}
	memory := observation.Response.Memory
	report := memoryReport{
		ObservedAtUnixMS: time.Now().UnixMilli(), Source: "cpp_backend_memory_snapshot", Unit: "bytes",
		BackendState: backendStateName(observation.Response.State), StatusRPCLatencyMS: observation.LatencyMS,
		ModelTensorBytes: memory.ModelTensorBytes, LlamaModelAllocatedBytes: memory.LlamaModelAllocatedBytes,
		LlamaContextAllocatedBytes: memory.LlamaContextAllocatedBytes, LlamaComputeAllocatedBytes: memory.LlamaComputeAllocatedBytes,
		ProcessSamples: make([]processMemoryReport, 0, len(memory.ProcessSamples)),
		LlamaBuffers:   make([]llamaBufferReport, 0, len(memory.LlamaBuffers)),
		Configuration:  memoryConfiguration{memory.ConfiguredContextTokens, memory.ConfiguredBatchTokens, memory.ConfiguredSequences, memory.ConfiguredGpuLayers},
	}
	for _, sample := range memory.ProcessSamples {
		report.ProcessSamples = append(report.ProcessSamples, processMemoryReport{
			Point: memoryPointName(sample.Point), Status: measurementStatusName(sample.Status), MonotonicNS: sample.MonotonicNs,
			ResidentBytes: sample.ResidentBytes, PeakResidentBytes: sample.PeakResidentBytes,
			PhysicalFootprintBytes: sample.PhysicalFootprintBytes, PeakPhysicalFootprintBytes: sample.PeakPhysicalFootprintBytes,
		})
	}
	for _, row := range memory.LlamaBuffers {
		report.LlamaBuffers = append(report.LlamaBuffers, llamaBufferReport{row.Name, row.ModelBytes, row.ContextBytes, row.ComputeBytes})
	}
	writeJSON(w, http.StatusOK, report)
}
