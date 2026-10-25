package service

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type reconfigurableBackend interface {
	Reconfigure(string, string, string, uint32, uint32, bool) error
}

func (s *Service) applyModelConfig(w http.ResponseWriter, r *http.Request) {
	managed, ok := s.managed.(reconfigurableBackend)
	if !ok {
		writeJSON(w, 409, fail("Managed backend cannot be reconfigured", "invalid_request_error", "backend_not_reconfigurable"))
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.RawQuery != "" {
		writeJSON(w, 400, fail("Apply takes no body or query", "invalid_request_error", "invalid_control_request"))
		return
	}
	if !s.lifecycleMu.TryLock() {
		writeJSON(w, 409, fail("Lifecycle operation in progress", "server_error", "lifecycle_busy"))
		return
	}
	defer s.lifecycleMu.Unlock()
	s.admissionMu.Lock()
	s.draining = true
	active := s.generationHandlers
	s.admissionMu.Unlock()
	if active != 0 {
		writeJSON(w, 409, fail("Admission paused; wait for active requests", "server_error", "generation_active"))
		return
	}
	path, e1 := readSmallFile(filepath.Join(s.modelConfigDir, "model.path"))
	id, e2 := readSmallFile(filepath.Join(s.modelConfigDir, "model.id"))
	batching, e3 := readSmallFile(filepath.Join(s.modelConfigDir, "batching"))
	if e1 != nil || e2 != nil || e3 != nil {
		writeJSON(w, 404, fail("No saved model configuration", "invalid_request_error", "model_config_not_found"))
		return
	}
	contextTokens, outputTokens := readBudget(s.modelConfigDir, "context.tokens", 4096), readBudget(s.modelConfigDir, "max-output.tokens", 512)
	if _, err := inspectModel(path, id, batching, contextTokens, outputTokens); err != nil {
		writeJSON(w, 422, fail(err.Error(), "invalid_request_error", "model_preflight_failed"))
		return
	}
	if err := s.managed.Stop(r.Context()); err != nil {
		writeJSON(w, 503, fail("Could not stop backend", "server_error", "backend_operation_failed"))
		return
	}
	qualified := false
	if batching == "continuous" {
		data, err := os.ReadFile(filepath.Join(s.modelConfigDir, "continuous-qualified"))
		info, statErr := os.Stat(path)
		if err == nil && statErr == nil {
			lines := strings.Split(string(data), "\n")
			qualified = len(lines) >= 2 && lines[0] == path && lines[1] == fmt.Sprintf("%d:%d", info.Size(), info.ModTime().Unix())
		}
	}
	if err := managed.Reconfigure(path, id, batching, contextTokens, outputTokens, qualified); err != nil {
		writeJSON(w, 422, fail("Saved runtime configuration is not applicable", "invalid_request_error", "model_config_apply_failed"))
		return
	}
	if err := s.managed.Start(r.Context()); err != nil {
		writeJSON(w, 503, fail("Could not start configured backend", "server_error", "backend_operation_failed"))
		return
	}
	// Preserve drain after restart so the operator can inspect readiness and
	// qualification before deliberately resuming generation traffic.
	writeJSON(w, 200, map[string]any{"state": "ready", "admission": "drained", "model_id": id, "batching": batching, "context_tokens": contextTokens, "max_output_tokens": outputTokens})
}

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
