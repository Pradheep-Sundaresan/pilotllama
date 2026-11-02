package service

import (
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "inferno.local/controlplane/gen/inferno/v1"
)

type schedulerReport struct {
	ObservedAtUnixMS   int64             `json:"observed_at_unix_ms"`
	Source             string            `json:"source"`
	Unit               string            `json:"unit"`
	BackendState       string            `json:"backend_state"`
	WaitingRequests    uint32            `json:"waiting_requests"`
	ExecutingRequests  uint32            `json:"executing_requests"`
	QueueCapacity      uint32            `json:"queue_capacity"`
	StatusRPCLatencyMS float64           `json:"status_rpc_latency_ms"`
	GoWaitingRequests  int               `json:"go_waiting_requests"`
	GoWaitingCapacity  int               `json:"go_waiting_capacity"`
	GoActiveHandlers   int               `json:"go_active_handlers"`
	GoActiveCapacity   int               `json:"go_active_capacity"`
	Outcomes           admissionOutcomes `json:"admission_outcomes"`
}

type admissionOutcomes struct {
	QueueFull            uint64 `json:"queue_full"`
	QueueExpired         uint64 `json:"queue_expired"`
	RateLimited          uint64 `json:"rate_limited"`
	CanceledWhileWaiting uint64 `json:"canceled_while_waiting"`
}

func backendStateName(state pb.BackendState) string {
	switch state {
	case pb.BackendState_BACKEND_STATE_READY:
		return "ready"
	case pb.BackendState_BACKEND_STATE_NO_MODEL:
		return "no_model"
	case pb.BackendState_BACKEND_STATE_LOADING:
		return "loading"
	case pb.BackendState_BACKEND_STATE_DRAINING:
		return "draining"
	case pb.BackendState_BACKEND_STATE_FAILED:
		return "failed"
	default:
		return "unknown"
	}
}

func (s *Service) schedulerStatus(w http.ResponseWriter, r *http.Request) {
	observation, err := s.rpc.Status(r.Context())
	if err != nil {
		code, body := mappedError(err)
		writeJSON(w, code, body)
		return
	}
	if observation.Response == nil || observation.Response.Scheduler == nil {
		code, body := mappedError(status.Error(codes.DataLoss, "missing scheduler snapshot"))
		writeJSON(w, code, body)
		return
	}
	snapshot := observation.Response.Scheduler
	s.queueMu.Lock()
	goWaiting := len(s.waiters)
	s.queueMu.Unlock()
	writeJSON(w, http.StatusOK, schedulerReport{
		ObservedAtUnixMS:   time.Now().UnixMilli(),
		Source:             "cpp_runtime_status_snapshot",
		Unit:               "requests",
		BackendState:       backendStateName(observation.Response.State),
		WaitingRequests:    snapshot.WaitingRequests,
		ExecutingRequests:  snapshot.ExecutingRequests,
		QueueCapacity:      snapshot.QueueCapacity,
		StatusRPCLatencyMS: observation.LatencyMS,
		GoWaitingRequests:  goWaiting,
		GoWaitingCapacity:  s.queueCapacity,
		GoActiveHandlers:   len(s.active),
		GoActiveCapacity:   cap(s.active),
		Outcomes: admissionOutcomes{
			QueueFull: s.queueFull.Load(), QueueExpired: s.queueExpired.Load(),
			RateLimited: s.rateLimited.Load(), CanceledWhileWaiting: s.queueCanceled.Load(),
		},
	})
}
