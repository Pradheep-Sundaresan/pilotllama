package service

import (
	"math"
	"net/http"
	"strconv"
	"time"
)

// acquireGeneration holds at most queueCapacity lightweight waiting handlers.
// A position is a snapshot at admission, not a continuously updated rank.
func (s *Service) acquireGeneration(w http.ResponseWriter, r *http.Request, id uint64) (time.Duration, bool) {
	started := time.Now()
	s.admissionMu.Lock()
	if s.drainSignal == nil {
		s.drainSignal = make(chan struct{})
	}
	drainSignal, draining := s.drainSignal, s.draining
	s.admissionMu.Unlock()
	if draining {
		writeJSON(w, 503, fail("Generation admission is draining", "server_error", "admission_draining"))
		return 0, false
	}
	select {
	case s.active <- struct{}{}:
		if s.admissionSnapshot().State != "accepting" {
			<-s.active
			writeJSON(w, 503, fail("Generation admission is draining", "server_error", "admission_draining"))
			return 0, false
		}
		return 0, true
	default:
	}
	if s.queueCapacity == 0 {
		s.queueFull.Add(1)
		writeJSON(w, 429, fail("Server is busy; retry later", "server_error", "server_busy"))
		return 0, false
	}
	s.queueMu.Lock()
	if s.waiters == nil {
		s.waiters = make(map[uint64]time.Time)
	}
	if len(s.waiters) >= s.queueCapacity {
		earliest := time.Now().Add(s.queueWaitTimeout)
		for _, deadline := range s.waiters {
			if deadline.Before(earliest) {
				earliest = deadline
			}
		}
		retry := int(math.Ceil(time.Until(earliest).Seconds()))
		if retry < 1 {
			retry = 1
		}
		s.queueMu.Unlock()
		s.queueFull.Add(1)
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeJSON(w, 429, fail("Generation waiting queue is full", "server_error", "queue_full"))
		return 0, false
	}
	position := len(s.waiters) + 1
	w.Header().Set("X-Queue-Position", strconv.Itoa(position))
	s.waiters[id] = time.Now().Add(s.queueWaitTimeout)
	s.queueMu.Unlock()
	timer := time.NewTimer(s.queueWaitTimeout)
	defer timer.Stop()
	defer func() { s.queueMu.Lock(); delete(s.waiters, id); s.queueMu.Unlock() }()
	select {
	case s.active <- struct{}{}:
		if s.admissionSnapshot().State != "accepting" {
			<-s.active
			writeJSON(w, 503, fail("Generation admission is draining", "server_error", "admission_draining"))
			return time.Since(started), false
		}
		return time.Since(started), true
	case <-r.Context().Done():
		s.queueCanceled.Add(1)
		writeJSON(w, 408, fail("Client canceled while waiting", "server_error", "queue_canceled"))
		return time.Since(started), false
	case <-timer.C:
		s.queueExpired.Add(1)
		writeJSON(w, 504, fail("Generation queue wait deadline exceeded", "server_error", "queue_wait_timeout"))
		return time.Since(started), false
	case <-drainSignal:
		writeJSON(w, 503, fail("Generation admission is draining", "server_error", "admission_draining"))
		return time.Since(started), false
	}
}
