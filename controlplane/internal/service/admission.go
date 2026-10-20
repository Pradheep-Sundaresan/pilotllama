package service

import "net/http"

// This is Go handler admission only. Zero handlers is not proof of native idle:
// a cancelled RPC can return before the C++ backend finishes cooperative cleanup.
type admissionState struct {
	State                    string `json:"state"`
	ActiveGenerationHandlers int    `json:"active_generation_handlers"`
	NativeIdle               *bool  `json:"native_idle"`
}

func (s *Service) admissionSnapshot() admissionState {
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	return s.admissionStateLocked()
}

func (s *Service) admissionStateLocked() admissionState {
	state := "accepting"
	if s.draining {
		state = "draining"
		if s.generationHandlers == 0 {
			state = "drained"
		}
	}
	return admissionState{State: state, ActiveGenerationHandlers: s.generationHandlers}
}

func (s *Service) generationAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.admissionMu.Lock()
		if s.draining {
			s.admissionMu.Unlock()
			writeJSON(w, 503, fail("Generation admission is draining", "server_error", "admission_draining"))
			return
		}
		s.generationHandlers++
		s.admissionMu.Unlock()
		defer func() { s.admissionMu.Lock(); s.generationHandlers--; s.admissionMu.Unlock() }()
		next.ServeHTTP(w, r)
	})
}

// Control endpoints fail closed even when generation's optional auth is disabled.
func (s *Service) control(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authDigest == nil {
			writeJSON(w, 403, fail("Admission controls require configured bearer authentication", "permission_error", "control_auth_required"))
			return
		}
		s.authenticate(next).ServeHTTP(w, r)
	})
}

func (s *Service) admissionStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.admissionSnapshot())
}

func (s *Service) setDraining(w http.ResponseWriter, r *http.Request, draining bool) {
	if !s.lifecycleMu.TryLock() {
		writeJSON(w, 409, fail("Lifecycle operation in progress", "server_error", "lifecycle_busy"))
		return
	}
	defer s.lifecycleMu.Unlock()
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.RawQuery != "" {
		writeJSON(w, 400, fail("Admission controls take no body or query parameters", "invalid_request_error", "invalid_control_request"))
		return
	}
	s.admissionMu.Lock()
	s.draining = draining
	state := s.admissionStateLocked()
	s.admissionMu.Unlock()
	writeJSON(w, 200, state)
}

func (s *Service) drain(w http.ResponseWriter, r *http.Request)  { s.setDraining(w, r, true) }
func (s *Service) resume(w http.ResponseWriter, r *http.Request) { s.setDraining(w, r, false) }
