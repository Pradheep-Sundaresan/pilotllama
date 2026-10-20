package service

import (
	"math"
	"net/http"
	"sort"
	"time"
)

type metricDistribution struct {
	Samples int      `json:"samples"`
	P50     *float64 `json:"p50"`
	P95     *float64 `json:"p95"`
	P99     *float64 `json:"p99"`
}

// Empirical nearest-rank quantiles; suppress tails below documented thresholds.
func distribution(values []float64) metricDistribution {
	valid := make([]float64, 0, len(values))
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 {
			valid = append(valid, v)
		}
	}
	sort.Float64s(valid)
	d := metricDistribution{Samples: len(valid)}
	if len(valid) > 0 {
		v := valid[int(math.Ceil(.50*float64(len(valid))))-1]
		d.P50 = &v
	}
	if len(valid) >= 20 {
		v := valid[int(math.Ceil(.95*float64(len(valid))))-1]
		d.P95 = &v
	}
	if len(valid) >= 100 {
		v := valid[int(math.Ceil(.99*float64(len(valid))))-1]
		d.P99 = &v
	}
	return d
}

type aggregateReport struct {
	WindowStartUnixMS              int64              `json:"window_start_unix_ms"`
	WindowEndUnixMS                int64              `json:"window_end_unix_ms"`
	WindowDurationMS               float64            `json:"window_duration_ms"`
	HistoryCapacity                int                `json:"history_capacity"`
	RetentionAtCapacity            bool               `json:"retention_at_capacity"`
	RetainedRecords                int                `json:"retained_records"`
	Samples                        int                `json:"samples"`
	Completed                      int                `json:"completed"`
	Failed                         int                `json:"failed"`
	DeliveryFailed                 int                `json:"delivery_failed"`
	CompletedNativeOutputTokens    uint64             `json:"completed_native_output_tokens"`
	CompletedNativeTokensPerSecond *float64           `json:"completed_native_tokens_per_second"`
	HandlerLatencyMS               metricDistribution `json:"handler_latency_ms"`
	RPCLatencyMS                   metricDistribution `json:"rpc_latency_ms"`
	RPCTTFTMS                      metricDistribution `json:"rpc_ttft_ms"`
	NativeRuntimeLatencyMS         metricDistribution `json:"native_runtime_latency_ms"`
	NativeTTFTMS                   metricDistribution `json:"native_ttft_ms"`
	NativeDecodeTokensPerSecond    metricDistribution `json:"native_decode_tokens_per_second"`
	QueueDepth                     *uint64            `json:"queue_depth"`
	MemoryBytes                    *uint64            `json:"memory_bytes"`
}

func summarize(records []requestRecord, since, now time.Time) aggregateReport {
	start := now.Add(-time.Minute)
	if since.After(start) {
		start = since
	}
	if start.After(now) {
		start = now
	}
	r := aggregateReport{WindowStartUnixMS: start.UnixMilli(), WindowEndUnixMS: now.UnixMilli(), WindowDurationMS: float64(now.Sub(start)) / float64(time.Millisecond), HistoryCapacity: requestHistoryCapacity, RetainedRecords: len(records), RetentionAtCapacity: len(records) == requestHistoryCapacity}
	var handler, rpcLatency, rpcTTFT, nativeLatency, nativeTTFT, decodeRate []float64
	add := func(values *[]float64, v *float64) {
		if v != nil {
			*values = append(*values, *v)
		}
	}
	for _, v := range records {
		if v.finished.IsZero() || v.finished.Before(start) || v.finished.After(now) {
			continue
		}
		r.Samples++
		switch v.Outcome {
		case "delivery_failed":
			r.DeliveryFailed++
			continue
		case "completed":
			r.Completed++
		default:
			r.Failed++
			continue
		}
		handler = append(handler, v.HandlerLatencyMS)
		add(&rpcLatency, v.RPCLatencyMS)
		add(&rpcTTFT, v.RPCTTFTMS)
		if v.Native != nil {
			r.CompletedNativeOutputTokens += uint64(v.Native.OutputTokens)
			add(&nativeLatency, v.Native.RuntimeLatencyMS)
			add(&nativeTTFT, v.Native.TTFTMS)
			add(&decodeRate, v.Native.DecodeTokensPerSecond)
		}
	}
	r.HandlerLatencyMS, r.RPCLatencyMS, r.RPCTTFTMS = distribution(handler), distribution(rpcLatency), distribution(rpcTTFT)
	r.NativeRuntimeLatencyMS, r.NativeTTFTMS, r.NativeDecodeTokensPerSecond = distribution(nativeLatency), distribution(nativeTTFT), distribution(decodeRate)
	if r.WindowDurationMS > 0 {
		rate := float64(r.CompletedNativeOutputTokens) / (r.WindowDurationMS / 1000)
		r.CompletedNativeTokensPerSecond = &rate
	}
	return r
}

func (s *Service) aggregateMetrics(w http.ResponseWriter, r *http.Request) {
	// Snapshot first; completion after the snapshot is omitted until the next poll.
	records := s.history.snapshot()
	writeJSON(w, 200, summarize(records, s.observedSince, time.Now()))
}
