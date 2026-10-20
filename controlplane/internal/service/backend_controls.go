package service

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Service) backendStatus(w http.ResponseWriter, r *http.Request) {
	state := "external"
	if s.managed != nil {
		state = s.managed.State()
	}
	writeJSON(w, 200, map[string]any{"state": state, "managed": s.managed != nil, "admission": s.admissionSnapshot()})
}

func (s *Service) backendOperation(w http.ResponseWriter, r *http.Request, start bool) {
	if s.managed == nil {
		writeJSON(w, 409, fail("External backend cannot be controlled", "invalid_request_error", "backend_not_managed"))
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.RawQuery != "" {
		writeJSON(w, 400, fail("Backend controls take no body or query parameters", "invalid_request_error", "invalid_control_request"))
		return
	}
	if !s.lifecycleMu.TryLock() {
		writeJSON(w, 409, fail("Lifecycle operation in progress", "server_error", "lifecycle_busy"))
		return
	}
	defer s.lifecycleMu.Unlock()
	// Control requests have no body. Clear the input deadline before model loading.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	s.admissionMu.Lock()
	s.draining = true
	active := s.generationHandlers
	s.admissionMu.Unlock()
	if active != 0 {
		writeJSON(w, 409, fail("Admission paused; wait for active Go handlers before retrying", "server_error", "generation_active"))
		return
	}
	var err error
	if start {
		err = s.managed.Start(r.Context())
	} else {
		err = s.managed.Stop(r.Context())
	}
	if err != nil {
		code := 503
		if status.Code(err) == codes.Aborted {
			code = 409
		}
		if err == context.Canceled || err == context.DeadlineExceeded {
			code = 504
		}
		writeJSON(w, code, fail("Managed backend operation failed; inspect backend status", "server_error", "backend_operation_failed"))
		return
	}
	// Start deliberately preserves drain. Resume remains an explicit user action.
	s.backendStatus(w, r)
}

func (s *Service) backendStart(w http.ResponseWriter, r *http.Request) {
	s.backendOperation(w, r, true)
}
func (s *Service) backendStop(w http.ResponseWriter, r *http.Request) {
	s.backendOperation(w, r, false)
}
