package rpcbackend_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"inferno.local/controlplane/internal/service"
)

func TestIntegrationPublicHTTPAuth(t *testing.T) {
	f := startFixture(t, false)
	const secret = "integration-test-secret-0123456789abcdef"
	s, err := service.New(service.Config{Listen: "127.0.0.1:8080", BackendGRPC: f.target, AuthToken: secret})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	for _, token := range []string{"", strings.Repeat("x", 32), secret} {
		for _, stream := range []bool{false, true} {
			data, err := json.Marshal(map[string]any{"model": "model-free-test-fixture", "messages": []any{map[string]string{"role": "user", "content": "tokens"}}, "max_tokens": 4, "stream": stream})
			if err != nil {
				t.Fatal(err)
			}
			r, err := http.NewRequestWithContext(contextFor(t, 5*time.Second), "POST", h.URL+"/v1/chat/completions", bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Content-Type", "application/json")
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			if token == secret {
				checkChat(t, resp, "😀!", stream)
				continue
			}
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Fatalf("unauthorized HTTP %d", resp.StatusCode)
			}
		}
	}
	r, err := http.NewRequestWithContext(contextFor(t, 5*time.Second), "GET", h.URL+"/internal/v1/requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+secret)
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var history struct {
		Capacity int
		Records  []struct {
			RequestID         string `json:"request_id"`
			Outcome           string
			ReceivedTokens    uint32   `json:"received_tokens"`
			TokenBytes        uint64   `json:"token_bytes"`
			RPCLatencyMS      *float64 `json:"rpc_latency_ms"`
			TransportComplete bool     `json:"transport_complete"`
			Native            *struct {
				OutputTokens     uint32   `json:"output_tokens"`
				RuntimeLatencyMS *float64 `json:"runtime_latency_ms"`
			}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || history.Capacity != 128 || len(history.Records) != 2 {
		t.Fatalf("history status=%d count=%d", resp.StatusCode, len(history.Records))
	}
	for _, record := range history.Records {
		if record.RequestID == "" || record.Outcome != "completed" || record.ReceivedTokens != 4 || record.TokenBytes != 5 || !record.TransportComplete || record.RPCLatencyMS == nil || record.Native == nil || record.Native.OutputTokens != 4 || record.Native.RuntimeLatencyMS == nil {
			t.Fatalf("invalid live fixture record: %+v", record)
		}
	}
	r, err = http.NewRequestWithContext(contextFor(t, 5*time.Second), "GET", h.URL+"/internal/v1/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+secret)
	metricsResponse, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer metricsResponse.Body.Close()
	var metrics struct {
		Samples   int
		Completed int
		Failed    int
		Tokens    uint64   `json:"completed_native_output_tokens"`
		Duration  float64  `json:"window_duration_ms"`
		Rate      *float64 `json:"completed_native_tokens_per_second"`
		Latency   struct {
			Samples int
			P95     *float64
		} `json:"handler_latency_ms"`
	}
	if err := json.NewDecoder(metricsResponse.Body).Decode(&metrics); err != nil {
		t.Fatal(err)
	}
	if metricsResponse.StatusCode != 200 || metrics.Samples != 2 || metrics.Completed != 2 || metrics.Failed != 0 || metrics.Tokens != 8 || metrics.Duration <= 0 || metrics.Rate == nil || metrics.Latency.Samples != 2 || metrics.Latency.P95 != nil {
		t.Fatalf("invalid live metrics: %+v", metrics)
	}
}

func TestIntegrationPublicHTTPRateLimit(t *testing.T) {
	f := startFixture(t, false)
	s, err := service.New(service.Config{Listen: "127.0.0.1:8080", BackendGRPC: f.target, RatePerMinute: 1, RateBurst: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	checkChat(t, postChat(t, contextFor(t, 5*time.Second), h.URL, "model-free-test-fixture", "tokens", false, 4), "😀!", false)
	for _, stream := range []bool{false, true} {
		resp := postChat(t, contextFor(t, 5*time.Second), h.URL, "model-free-test-fixture", "tokens", stream, 4)
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "60" || !strings.Contains(string(data), `"rate_limit_exceeded"`) || strings.Contains(string(data), "[DONE]") {
			t.Fatalf("rate response status=%d body=%s err=%v", resp.StatusCode, data, err)
		}
	}
}

func TestIntegrationPublicModelDiscovery(t *testing.T) {
	f := startFixture(t, false)
	s, err := service.New(service.Config{Listen: "127.0.0.1:8080", BackendGRPC: f.target})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	var alias string
	for _, path := range []string{"/v1/models", "/v1/models/model-free-test-fixture", "/v1/models/missing"} {
		r, err := http.NewRequestWithContext(contextFor(t, 5*time.Second), "GET", h.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(path, "missing") {
			if resp.StatusCode != 404 {
				t.Fatal(resp.StatusCode)
			}
			continue
		}
		if resp.StatusCode != 200 {
			t.Fatalf("discovery %d %s", resp.StatusCode, data)
		}
		if path == "/v1/models" {
			var v struct {
				Object string
				Data   []struct {
					ID      string
					Created *int64
				}
			}
			if err := json.Unmarshal(data, &v); err != nil || v.Object != "list" || len(v.Data) != 1 || v.Data[0].Created != nil {
				t.Fatalf("invalid catalog: %s err=%v", data, err)
			}
			alias = v.Data[0].ID
		} else {
			var v struct {
				ID     string
				Object string
			}
			if err := json.Unmarshal(data, &v); err != nil || v.ID != alias || v.Object != "model" {
				t.Fatalf("invalid detail: %s", data)
			}
		}
	}
	checkChat(t, postChat(t, contextFor(t, 5*time.Second), h.URL, alias, "tokens", false, 4), "😀!", false)
}

func TestIntegrationAdmissionDrainResume(t *testing.T) {
	f := startFixture(t, false)
	const secret = "integration-control-secret-0123456789abcdef"
	s, err := service.New(service.Config{Listen: "127.0.0.1:8080", BackendGRPC: f.target, AuthToken: secret})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := httptest.NewServer(s.Handler())
	defer h.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	request := func(method, path, data string) *http.Response {
		r, err := http.NewRequestWithContext(contextFor(t, 5*time.Second), method, h.URL+path, strings.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+secret)
		if data != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	for _, action := range []string{"drain", "resume"} {
		resp := request("POST", "/internal/v1/admission/"+action, "")
		var state struct {
			State      string
			NativeIdle *bool `json:"native_idle"`
		}
		err := json.NewDecoder(resp.Body).Decode(&state)
		resp.Body.Close()
		want := "drained"
		if action == "resume" {
			want = "accepting"
		}
		if err != nil || resp.StatusCode != 200 || state.State != want || state.NativeIdle != nil {
			t.Fatalf("control state=%+v status=%d err=%v", state, resp.StatusCode, err)
		}
		data := `{"model":"model-free-test-fixture","messages":[{"role":"user","content":"tokens"}],"max_tokens":4}`
		resp = request("POST", "/v1/chat/completions", data)
		if action == "resume" {
			checkChat(t, resp, "😀!", false)
		} else {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || resp.StatusCode != 503 || !strings.Contains(string(body), "admission_draining") {
				t.Fatalf("drain generation status=%d body=%s err=%v", resp.StatusCode, body, err)
			}
		}
	}
}

func postChat(t *testing.T, ctx context.Context, url, model, prompt string, stream bool, limit int) *http.Response {
	t.Helper()
	data, err := json.Marshal(map[string]any{"model": model, "messages": []any{map[string]string{"role": "user", "content": prompt}}, "stream": stream, "max_completion_tokens": limit, "temperature": 0, "n": 1})
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.NewRequestWithContext(ctx, "POST", url+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}
func checkChat(t *testing.T, response *http.Response, want string, stream bool) {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("HTTP %d: %s", response.StatusCode, data)
	}
	id := "chatcmpl-" + response.Header.Get("X-Request-ID")
	if !stream {
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		choice := value["choices"].([]any)[0].(map[string]any)
		if value["id"] != id || value["object"] != "chat.completion" || choice["message"].(map[string]any)["content"] != want || value["usage"] != nil {
			t.Fatal(value)
		}
		return
	}
	var text strings.Builder
	done, finish, role := 0, 0, 0
	for _, record := range strings.Split(strings.TrimSpace(string(data)), "\n\n") {
		payload := strings.TrimPrefix(record, "data: ")
		if payload == "[DONE]" {
			done++
			continue
		}
		var value map[string]any
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			t.Fatal(err)
		}
		if value["object"] != "chat.completion.chunk" || value["id"] != id || value["usage"] != nil {
			t.Fatal(value)
		}
		choice := value["choices"].([]any)[0].(map[string]any)
		delta := choice["delta"].(map[string]any)
		if delta["role"] == "assistant" {
			role++
		}
		if content, ok := delta["content"].(string); ok {
			text.WriteString(content)
		}
		if choice["finish_reason"] != nil {
			finish++
		}
	}
	if text.String() != want || done != 1 || finish != 1 || role != 1 {
		t.Fatalf("SSE text=%q done=%d finish=%d role=%d", text.String(), done, finish, role)
	}
}

func TestIntegrationPublicHTTP(t *testing.T) {
	f := startFixture(t, false)
	s, err := service.New(service.Config{Listen: "127.0.0.1:8080", BackendGRPC: f.target, RequestTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	httpServer := httptest.NewServer(s.Handler())
	defer httpServer.Close()
	for _, stream := range []bool{false, true} {
		checkChat(t, postChat(t, contextFor(t, time.Second), httpServer.URL, "model-free-test-fixture", "tokens", stream, 8), "😀!", stream)
	}
	for _, tc := range []struct {
		prompt string
		stream bool
		code   int
	}{{"failure", false, 502}, {"failure", true, 502}, {"partial-failure", false, 502}, {"partial-failure", true, 200}} {
		response := postChat(t, contextFor(t, time.Second), httpServer.URL, "model-free-test-fixture", tc.prompt, tc.stream, 8)
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != tc.code || !bytes.Contains(data, []byte(`"error"`)) || bytes.Contains(data, []byte("[DONE]")) || bytes.Contains(data, []byte("secret")) {
			t.Fatalf("failure %d %s", response.StatusCode, data)
		}
	}
	response := postChat(t, contextFor(t, time.Second), httpServer.URL, "model-free-test-fixture", "gate", false, 8)
	if response.StatusCode != 504 {
		t.Fatal(response.StatusCode)
	}
	_ = response.Body.Close()
	if err := f.process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	checkChat(t, postChat(t, contextFor(t, time.Second), httpServer.URL, "model-free-test-fixture", "tokens", false, 8), "😀!", false)
	ctx, cancel := context.WithCancel(context.Background())
	response = postChat(t, ctx, httpServer.URL, "model-free-test-fixture", "flood", true, 512)
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	if _, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = response.Body.Close()
	checkChat(t, postChat(t, contextFor(t, time.Second), httpServer.URL, "model-free-test-fixture", "tokens", false, 8), "😀!", false)
}

func TestIntegrationHTTPShutdown(t *testing.T) {
	f := startFixture(t, false)
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reservation.Addr().String()
	_ = reservation.Close()
	s, err := service.New(service.Config{Listen: address, BackendGRPC: f.target})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() { runErr = s.Run(ctx, address); close(done) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			t.Error("Go HTTP shutdown did not join")
			return
		}
		s.Close()
		if runErr != nil {
			t.Error(runErr)
		}
	})
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 100 * time.Millisecond}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	startup := time.NewTimer(2 * time.Second)
	defer startup.Stop()
wait:
	for {
		select {
		case <-done:
			t.Fatalf("Go HTTP startup: %v", runErr)
		case <-startup.C:
			t.Fatal("Go HTTP startup timeout")
		case <-ticker.C:
			response, err := client.Get("http://" + address + "/healthz")
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode == 200 {
					break wait
				}
			}
		}
	}
	response := postChat(t, contextFor(t, 3*time.Second), "http://"+address, "model-free-test-fixture", "gate", true, 8)
	reader := bufio.NewReader(response.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	stop() // Cancels HTTP request -> gRPC while native fixture work is still gated.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Go shutdown waited for non-preemptible backend work")
	}
	data, err := io.ReadAll(reader)
	_ = response.Body.Close()
	if err != nil || bytes.Contains(data, []byte("[DONE]")) || !bytes.Contains(data, []byte(`"error"`)) {
		t.Fatalf("shutdown stream=%s err=%v", data, err)
	}
	if err := f.process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
}
