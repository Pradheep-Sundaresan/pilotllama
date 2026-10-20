package service

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"inferno.local/controlplane/internal/rpcbackend"
)

const requestHistoryCapacity = 128

type nativeMeasurements struct {
	OutputTokens          uint32   `json:"output_tokens"`
	RuntimeLatencyMS      *float64 `json:"runtime_latency_ms"`
	QueueWaitMS           *float64 `json:"queue_wait_ms"`
	BackendExecutionMS    *float64 `json:"backend_execution_ms"`
	PrefillPhaseMS        *float64 `json:"prefill_phase_ms"`
	DecodePhaseMS         *float64 `json:"decode_phase_ms"`
	TTFTMS                *float64 `json:"ttft_ms"`
	MeanInterTokenMS      *float64 `json:"mean_inter_token_ms"`
	DecodeTokensPerSecond *float64 `json:"decode_tokens_per_second"`
}

type requestRecord struct {
	finished               time.Time           // Private monotonic completion clock for aggregation.
	RequestID              string              `json:"request_id"`
	FinishedAtUnixMS       int64               `json:"finished_at_unix_ms"`
	Outcome                string              `json:"outcome"`
	HandlerLatencyMS       float64             `json:"handler_latency_ms"`
	RPCLatencyMS           *float64            `json:"rpc_latency_ms"`
	RPCTTFTMS              *float64            `json:"rpc_ttft_ms"`
	ReceivedTokens         uint32              `json:"received_tokens"`
	TokenBytes             uint64              `json:"token_bytes"`
	ReceiveTokensPerSecond *float64            `json:"receive_tokens_per_second"`
	RPCCode                *string             `json:"rpc_code"`
	TransportComplete      bool                `json:"transport_complete"`
	NativeState            *string             `json:"native_state"`
	Native                 *nativeMeasurements `json:"native"`
	StreamWriteFailed      *bool               `json:"stream_write_failed"`
}

type requestHistory struct {
	mu          sync.Mutex
	entries     [requestHistoryCapacity]requestRecord
	next, count int
}

func (h *requestHistory) add(v requestRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries[h.next] = v
	h.next = (h.next + 1) % requestHistoryCapacity
	if h.count < requestHistoryCapacity {
		h.count++
	}
}

func (h *requestHistory) snapshot() []requestRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]requestRecord, h.count)
	for i := range result {
		result[i] = h.entries[(h.next-1-i+requestHistoryCapacity)%requestHistoryCapacity]
	}
	return result
}

func copyMeasurement(v *float64) *float64 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func makeRecord(id uint64, started time.Time, outcome string, observation rpcbackend.Observation, streamFailure *bool) requestRecord {
	finished := time.Now()
	r := requestRecord{finished: finished, RequestID: strconv.FormatUint(id, 10), FinishedAtUnixMS: finished.UnixMilli(), Outcome: outcome,
		HandlerLatencyMS: float64(finished.Sub(started)) / float64(time.Millisecond), RPCLatencyMS: copyMeasurement(observation.LatencyMS),
		RPCTTFTMS: copyMeasurement(observation.TTFTMS), ReceivedTokens: observation.ReceivedTokens, TokenBytes: observation.TokenBytes,
		ReceiveTokensPerSecond: copyMeasurement(observation.ReceiveTokensPerSecond), TransportComplete: observation.TransportComplete}
	if streamFailure != nil {
		c := *streamFailure
		r.StreamWriteFailed = &c
	}
	if observation.RPCCode != nil {
		c := observation.RPCCode.String()
		r.RPCCode = &c
	}
	if observation.Finished != nil {
		state := observation.Finished.State.String()
		r.NativeState = &state
		if m := observation.Finished.Metrics; m != nil {
			r.Native = &nativeMeasurements{m.OutputTokens, copyMeasurement(m.RuntimeLatencyMs), copyMeasurement(m.QueueWaitMs),
				copyMeasurement(m.BackendExecutionMs), copyMeasurement(m.PrefillPhaseMs), copyMeasurement(m.DecodePhaseMs),
				copyMeasurement(m.TtftMs), copyMeasurement(m.MeanInterTokenMs), copyMeasurement(m.DecodeTokensPerSecond)}
		}
	}
	return r
}

func (s *Service) requestHistoryHandler(w http.ResponseWriter, r *http.Request) {
	records := s.history.snapshot()
	if id := r.PathValue("request"); id != "" {
		for _, record := range records {
			if record.RequestID == id {
				writeJSON(w, 200, record)
				return
			}
		}
		writeJSON(w, 404, fail("Request record not retained", "invalid_request_error", "request_not_found"))
		return
	}
	writeJSON(w, 200, struct {
		Capacity int             `json:"capacity"`
		Records  []requestRecord `json:"records"`
	}{requestHistoryCapacity, records})
}
