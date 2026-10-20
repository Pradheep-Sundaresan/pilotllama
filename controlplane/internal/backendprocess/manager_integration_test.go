package backendprocess

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"inferno.local/controlplane/internal/service"
)

func TestIntegrationManagerHTTPStopStart(t *testing.T) {
	fixture := os.Getenv("INFERNO_RPC_FIXTURE")
	if fixture == "" {
		t.Skip("explicit model-free C++ fixture required")
	}
	c := config(t)
	c.Executable, c.ModelID = fixture, "model-free-test-fixture"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m, err := newManager(ctx, c, func(exe string, args ...string) *exec.Cmd { return exec.Command(exe, args[7]) }, ready)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	const secret = "managed-http-test-secret-0123456789abcdef"
	s, err := service.New(service.Config{Listen: "127.0.0.1:8080", ManagedBackend: m, AuthToken: secret, RatePerMinute: 1, RateBurst: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	request := func(method, path, body string, want int) string {
		r, err := http.NewRequestWithContext(ctx, method, h.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+secret)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s: HTTP %d body=%s err=%v", path, resp.StatusCode, data, err)
		}
		return string(data)
	}
	body := `{"model":"model-free-test-fixture","messages":[{"role":"user","content":"tokens"}],"max_tokens":4}`
	unauthorized, err := http.NewRequestWithContext(ctx, "POST", h.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Header.Set("Content-Type", "application/json")
	denied, err := client.Do(unauthorized)
	if err != nil {
		t.Fatal(err)
	}
	denied.Body.Close()
	if denied.StatusCode != 401 {
		t.Fatal("combined auth rejection")
	}
	if result := request("GET", "/v1/models", "", 200); !strings.Contains(result, "model-free-test-fixture") {
		t.Fatal("configured model not discoverable")
	}
	if result := request("POST", "/v1/chat/completions", body, 200); !strings.Contains(result, "😀!") {
		t.Fatal(result)
	}
	request("POST", "/internal/v1/backend/stop", "", 200)
	if m.State() != "stopped" {
		t.Fatal(m.State())
	}
	request("GET", "/healthz", "", 200)
	request("GET", "/readyz", "", 503)
	request("GET", "/v1/models", "", 503)
	request("POST", "/v1/chat/completions", body, 503)
	request("POST", "/internal/v1/backend/start", "", 200)
	if m.State() != "ready" {
		t.Fatal(m.State())
	}
	request("GET", "/readyz", "", 503) // Start preserves explicit admission drain.
	request("POST", "/internal/v1/admission/resume", "", 200)
	request("GET", "/readyz", "", 200)
	stream := strings.TrimSuffix(body, "}") + `,"stream":true}`
	if result := request("POST", "/v1/chat/completions", stream, 200); !strings.Contains(result, "😀") || !strings.Contains(result, "[DONE]") {
		t.Fatal(result)
	}
	if result := request("POST", "/v1/chat/completions", body, 429); !strings.Contains(result, "rate_limit_exceeded") {
		t.Fatal("combined rate rejection")
	}
	request("POST", "/internal/v1/backend/stop", "", 200)
	request("GET", "/healthz", "", 200)
	var history struct{ Records []struct{ Outcome string } }
	if err := json.Unmarshal([]byte(request("GET", "/internal/v1/requests", "", 200)), &history); err != nil || len(history.Records) != 2 {
		t.Fatalf("cross-restart history=%+v err=%v", history, err)
	}
	var metrics struct {
		Samples, Completed int
		Tokens             uint64 `json:"completed_native_output_tokens"`
	}
	if err := json.Unmarshal([]byte(request("GET", "/internal/v1/metrics", "", 200)), &metrics); err != nil || metrics.Samples != 2 || metrics.Completed != 2 || metrics.Tokens != 8 {
		t.Fatalf("combined metrics=%+v err=%v", metrics, err)
	}
	entries, err := os.ReadDir(c.StateDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("owned process state not cleaned")
	}
}
