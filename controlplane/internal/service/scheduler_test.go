package service

import (
	"encoding/json"
	"testing"
	"time"

	pb "inferno.local/controlplane/gen/inferno/v1"
)

func TestSchedulerEndpointReportsAuthenticatedRuntimeSnapshot(t *testing.T) {
	s, backend := authenticatedService(t, testSecret)
	backend.scheduler = &pb.SchedulerSnapshot{WaitingRequests: 2, ExecutingRequests: 1, QueueCapacity: 4}
	s.active <- struct{}{}
	s.queueMu.Lock()
	s.waiters[42] = time.Now().Add(time.Second)
	s.queueMu.Unlock()
	s.queueFull.Store(3)
	s.queueExpired.Store(2)
	s.rateLimited.Store(1)
	s.queueCanceled.Store(4)
	w := controlRequest(s, "GET", "/internal/v1/scheduler", testSecret)
	var report schedulerReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || report.Source != "cpp_runtime_status_snapshot" || report.Unit != "requests" ||
		report.BackendState != "ready" || report.WaitingRequests != 2 || report.ExecutingRequests != 1 ||
		report.QueueCapacity != 4 || report.GoWaitingRequests != 1 || report.GoWaitingCapacity != s.queueCapacity ||
		report.GoActiveHandlers != 1 || report.GoActiveCapacity != 2 || report.Outcomes.QueueFull != 3 ||
		report.Outcomes.QueueExpired != 2 || report.Outcomes.RateLimited != 1 || report.Outcomes.CanceledWhileWaiting != 4 ||
		report.ObservedAtUnixMS == 0 || backend.statusCalls != 1 || backend.generationCalls != 0 {
		t.Fatalf("status=%d report=%+v backend=%+v", w.Code, report, backend)
	}
	if w := controlRequest(s, "GET", "/internal/v1/scheduler", ""); w.Code != 401 {
		t.Fatal("scheduler auth bypassed")
	}
}

func TestSchedulerEndpointRejectsUnavailableMeasurement(t *testing.T) {
	s, _ := authenticatedService(t, testSecret)
	if w := controlRequest(s, "GET", "/internal/v1/scheduler", testSecret); w.Code != 502 {
		t.Fatalf("missing scheduler status=%d body=%s", w.Code, w.Body)
	}
	s.authDigest = nil
	if w := controlRequest(s, "GET", "/internal/v1/scheduler", testSecret); w.Code != 403 {
		t.Fatal("scheduler available without configured control auth")
	}
}
