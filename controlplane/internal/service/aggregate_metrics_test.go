package service

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestAggregateQuantilesAndMissingSamples(t *testing.T) {
	if v := distribution(nil); v.Samples != 0 || v.P50 != nil || v.P95 != nil || v.P99 != nil {
		t.Fatal("empty percentiles fabricated")
	}
	values := make([]float64, 100)
	for i := range values {
		values[i] = float64(100 - i)
	}
	v := distribution(values)
	if v.Samples != 100 || *v.P50 != 50 || *v.P95 != 95 || *v.P99 != 99 {
		t.Fatal(v)
	}
	if v := distribution(values[:19]); v.P95 != nil || v.P99 != nil {
		t.Fatal("insufficient tails exposed")
	}
	if v := distribution(values[:20]); v.P95 == nil || v.P99 != nil {
		t.Fatal("tail threshold")
	}
	if v := distribution([]float64{math.NaN(), math.Inf(1), -1, 0}); v.Samples != 1 || *v.P50 != 0 {
		t.Fatal("nonfinite sample not excluded")
	}
}

func TestAggregateWindowAndOutcomeSeparation(t *testing.T) {
	now := time.Now()
	nativeTime := 12.0
	completed := requestRecord{finished: now.Add(-time.Second), Outcome: "completed", HandlerLatencyMS: 30,
		Native: &nativeMeasurements{OutputTokens: 60, TTFTMS: &nativeTime}}
	failed := completed
	failed.Outcome = "failed"
	delivery := completed
	delivery.Outcome = "delivery_failed"
	old := completed
	old.finished = now.Add(-61 * time.Second)
	future := completed
	future.finished = now.Add(time.Second)
	r := summarize([]requestRecord{completed, failed, delivery, old, future}, now.Add(-time.Hour), now)
	if r.Samples != 3 || r.Completed != 1 || r.Failed != 1 || r.DeliveryFailed != 1 || r.CompletedNativeOutputTokens != 60 || r.WindowDurationMS != 60000 || *r.CompletedNativeTokensPerSecond != 1 {
		t.Fatalf("bad aggregate: %+v", r)
	}
	if r.HandlerLatencyMS.Samples != 1 || *r.HandlerLatencyMS.P50 != 30 || r.HandlerLatencyMS.P95 != nil || r.NativeTTFTMS.Samples != 1 || *r.NativeTTFTMS.P50 != 12 || r.RPCTTFTMS.Samples != 0 || r.QueueDepth != nil || r.MemoryBytes != nil {
		t.Fatal("measurement sources mixed")
	}
	r = summarize([]requestRecord{completed}, now.Add(-2*time.Second), now)
	if r.WindowDurationMS != 2000 || *r.CompletedNativeTokensPerSecond != 30 {
		t.Fatal("startup observation window")
	}
	r = summarize(nil, now, now)
	if r.CompletedNativeTokensPerSecond != nil {
		t.Fatal("zero duration rate")
	}
	r = summarize(make([]requestRecord, requestHistoryCapacity), now.Add(-time.Minute), now)
	if !r.RetentionAtCapacity || r.RetainedRecords != requestHistoryCapacity || r.Samples != 0 {
		t.Fatal("retention capacity qualification")
	}
}

func TestAggregateEndpointAuthAndRingWindow(t *testing.T) {
	s, _ := authenticatedService(t, testSecret)
	if w := controlRequest(s, "GET", "/internal/v1/metrics", ""); w.Code != 401 {
		t.Fatal("metrics auth bypassed")
	}
	s.observedSince = time.Now().Add(-time.Minute)
	for i := 0; i < 200; i++ {
		s.history.add(requestRecord{finished: time.Now(), Outcome: "completed", HandlerLatencyMS: float64(i), Native: &nativeMeasurements{OutputTokens: 1}})
	}
	w := controlRequest(s, "GET", "/internal/v1/metrics", testSecret)
	var r aggregateReport
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || r.Completed != 128 || !r.RetentionAtCapacity || r.HandlerLatencyMS.Samples != 128 || r.CompletedNativeOutputTokens != 128 || r.HandlerLatencyMS.P99 == nil {
		t.Fatalf("aggregate %d %s", w.Code, w.Body)
	}
	s.authDigest = nil
	if w := controlRequest(s, "GET", "/internal/v1/metrics", testSecret); w.Code != 403 {
		t.Fatal("metrics without configured auth")
	}
}
