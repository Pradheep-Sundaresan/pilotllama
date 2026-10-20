package service

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// One process-local token bucket, shared by all generation clients. No identity
// map, timers, waiting queue, or background goroutine is needed.
type requestLimiter struct {
	mu                          sync.Mutex
	perSecond, capacity, tokens float64
	last                        time.Time
}

func newRequestLimiter(perMinute, burst int, now time.Time) *requestLimiter {
	return &requestLimiter{perSecond: float64(perMinute) / 60, capacity: float64(burst), tokens: float64(burst), last: now}
}

// Returns zero when admitted, otherwise whole seconds until one token refills.
func (l *requestLimiter) take(now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.After(l.last) {
		l.tokens = math.Min(l.capacity, l.tokens+now.Sub(l.last).Seconds()*l.perSecond)
		l.last = now
	}
	if l.tokens >= 1 {
		l.tokens--
		return 0
	}
	return max(1, int(math.Ceil((1-l.tokens)/l.perSecond)))
}

func (s *Service) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.limiter != nil {
			if seconds := s.limiter.take(time.Now()); seconds > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(seconds))
				writeJSON(w, http.StatusTooManyRequests, fail("Generation request rate exceeded", "rate_limit_error", "rate_limit_exceeded"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
