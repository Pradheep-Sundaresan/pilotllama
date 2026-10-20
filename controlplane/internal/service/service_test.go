package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfig(t *testing.T) {
	for _, tc := range []struct {
		name, listen, backend string
		valid                 bool
	}{
		{"default", "127.0.0.1:8080", "http://127.0.0.1:8081", true},
		{"ipv6", "[::1]:8080", "http://[::1]:8081/", true},
		{"wildcard", "0.0.0.0:8080", "http://127.0.0.1:8081", false},
		{"hostname", "localhost:8080", "http://127.0.0.1:8081", false},
		{"zero-port", "127.0.0.1:0", "http://127.0.0.1:8081", false},
		{"port-range", "127.0.0.1:65536", "http://127.0.0.1:8081", false},
		{"remote-backend", "127.0.0.1:8080", "http://192.0.2.1:8081", false},
		{"proxy-hostname", "127.0.0.1:8080", "http://localhost:8081", false},
		{"https", "127.0.0.1:8080", "https://127.0.0.1:8081", false},
		{"credentials", "127.0.0.1:8080", "http://user:pass@127.0.0.1:8081", false},
		{"path", "127.0.0.1:8080", "http://127.0.0.1:8081/api", false},
		{"query", "127.0.0.1:8080", "http://127.0.0.1:8081?x=1", false},
		{"empty-query", "127.0.0.1:8080", "http://127.0.0.1:8081?", false},
		{"fragment", "127.0.0.1:8080", "http://127.0.0.1:8081#x", false},
		{"bad-url", "127.0.0.1:8080", "http://%", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{Listen: tc.listen, Backend: tc.backend})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestGRPCConfig(t *testing.T) {
	for _, tc := range []struct {
		target, legacy string
		valid          bool
	}{
		{"127.0.0.1:8081", "", true}, {"[::1]:8081", "", true},
		{"localhost:8081", "", false}, {"0.0.0.0:8081", "", false},
		{"127.0.0.1:8081", "http://127.0.0.1:8081", false},
	} {
		s, err := New(Config{Listen: "127.0.0.1:8080", Backend: tc.legacy, BackendGRPC: tc.target})
		if (err == nil) != tc.valid {
			t.Fatalf("target=%q legacy=%q err=%v", tc.target, tc.legacy, err)
		}
		if s != nil {
			s.Close()
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadiness(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		status         int
		failure, ready bool
	}{
		{"ready", `{"api_version":"1","ready":true}`, 200, false, true},
		{"not-loaded", `{"api_version":"1","ready":false}`, 200, false, false},
		{"unavailable", `{"api_version":"1","ready":true}`, 503, false, false},
		{"redirect", "", 302, false, false},
		{"wrong-version", `{"api_version":"2","ready":true}`, 200, false, false},
		{"missing-version", `{"ready":true}`, 200, false, false},
		{"missing-ready", `{"api_version":"1"}`, 200, false, false},
		{"malformed", `{`, 200, false, false},
		{"trailing-data", `{"api_version":"1","ready":true}{}`, 200, false, false},
		{"oversize", strings.Repeat(" ", 4097), 200, false, false},
		{"connection-error", "", 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := New(Config{Listen: "127.0.0.1:8080", Backend: "http://127.0.0.1:8081/"})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "http://127.0.0.1:8081/internal/v1/ready" || r.Method != "GET" {
					t.Fatalf("unexpected probe: %v", r)
				}
				if _, ok := r.Context().Deadline(); !ok {
					t.Error("probe has no deadline")
				}
				if tc.failure {
					return nil, errors.New("connection refused")
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
			want := http.StatusServiceUnavailable
			if tc.ready {
				want = http.StatusOK
			}
			if w.Code != want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if !strings.Contains(w.Body.String(), `"ready":`+map[bool]string{true: "true", false: "false"}[tc.ready]) {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func TestRoutes(t *testing.T) {
	s, err := New(Config{Listen: "127.0.0.1:8080", Backend: "http://127.0.0.1:8081"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("liveness must not probe backend")
		return nil, nil
	})
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/healthz", 200}, {"HEAD", "/healthz", 200},
		{"POST", "/healthz", 405}, {"POST", "/readyz", 405},
		{"GET", "/unknown", 404}, {"POST", "/v1/chat/completions", 404},
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}
		if tc.status == 200 && (w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store") {
			t.Error("health headers missing")
		}
	}
}

func TestStudioRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>studio</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("app"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Listen: "127.0.0.1:8080", Backend: "http://127.0.0.1:8081", StudioDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		method, target string
		status         int
		contains       string
	}{
		{"GET", "/", 200, "studio"},
		{"GET", "/playground", 200, "studio"},
		{"GET", "/assets/app.js", 200, "app"},
		{"HEAD", "/", 200, ""},
		{"GET", "/v1/missing", 404, ""},
		{"GET", "/v1", 404, ""},
		{"GET", "/internal/missing", 404, ""},
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(tc.method, tc.target, nil))
		if w.Code != tc.status || (tc.contains != "" && !strings.Contains(w.Body.String(), tc.contains)) {
			t.Errorf("%s %s = %d %q", tc.method, tc.target, w.Code, w.Body.String())
		}
	}
}

func TestStudioConfigRejectsMissingRelativeAndSymlink(t *testing.T) {
	base := Config{Listen: "127.0.0.1:8080", Backend: "http://127.0.0.1:8081"}
	base.StudioDir = "relative"
	if _, err := New(base); err == nil {
		t.Fatal("relative Studio path accepted")
	}
	base.StudioDir = t.TempDir()
	if _, err := New(base); err == nil {
		t.Fatal("missing index accepted")
	}
	if err := os.WriteFile(filepath.Join(base.StudioDir, "index.html"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base.StudioDir, "index.html"), filepath.Join(base.StudioDir, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(base); err == nil {
		t.Fatal("symlinked asset accepted")
	}
}

func TestProbeCancellation(t *testing.T) {
	s, err := New(Config{Listen: "127.0.0.1:8080", Backend: "http://127.0.0.1:8081"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	if s.backendReady(ctx) {
		t.Fatal("cancelled probe reported ready")
	}
	if !errors.Is(s.Run(ctx, "127.0.0.1:8080"), context.Canceled) {
		t.Fatal("cancelled startup must not listen")
	}
}

func TestClientRestrictions(t *testing.T) {
	s, err := New(Config{Listen: "127.0.0.1:8080", Backend: "http://127.0.0.1:8081"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	transport := s.client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.MaxConnsPerHost != 8 || s.client.Timeout <= 0 {
		t.Fatal("client bounds/proxy configuration changed")
	}
	if s.client.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects must not be followed")
	}
	if s.Run(context.Background(), "0.0.0.0:8080") == nil {
		t.Fatal("unsafe listen accepted")
	}
}
