package backendprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"inferno.local/controlplane/internal/service"
)

// Opt-in: production process manager, native Metal backend, and real HTTP sockets.
func TestNativeManagedLifecycle(t *testing.T) {
	binary, model := os.Getenv("INFERNO_NATIVE_RPC_SERVER"), os.Getenv("INFERNO_NATIVE_RPC_MODEL")
	if binary == "" || model == "" {
		t.Skip("opt in with scripts/test-native-managed.sh EXISTING_LOCAL_GGUF")
	}
	c := Config{Executable: binary, Model: model, ModelID: "local-nemotron-4b", StateDir: t.TempDir(), StartupTimeout: 30 * time.Second, ShutdownTimeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	requests := 0
	manager, err := NewManager(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	err = func(ctx context.Context) error {
		defer cancel()
		const secret = "native-lifecycle-test-secret-0123456789abcdef"
		s, err := service.New(service.Config{Listen: "127.0.0.1:8080", ManagedBackend: manager, AuthToken: secret, RequestTimeout: 10 * time.Second, RatePerMinute: 1, RateBurst: 4})
		if err != nil {
			return err
		}
		defer s.Close()
		// This server supplies real HTTP sockets without reserving a fixed port.
		h := httptest.NewServer(s.Handler())
		defer h.Close()
		client := &http.Client{Timeout: 45 * time.Second}
		defer client.CloseIdleConnections()
		request := func(method, path, body string) (*http.Response, error) {
			r, err := http.NewRequestWithContext(ctx, method, h.URL+path, strings.NewReader(body))
			if err != nil {
				return nil, err
			}
			if body != "" {
				r.Header.Set("Content-Type", "application/json")
			}
			r.Header.Set("Authorization", "Bearer "+secret)
			return client.Do(r)
		}
		expect := func(method, path string, want int) error {
			resp, err := request(method, path, "")
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != want {
				return fmt.Errorf("%s: HTTP %d, want %d", path, resp.StatusCode, want)
			}
			return nil
		}
		for _, path := range []string{"/healthz", "/readyz"} {
			resp, err := request("GET", path, "")
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
			}
		}
		for cycle := 0; cycle < 2; cycle++ {
			for _, stream := range []bool{false, true} {
				body := fmt.Sprintf(`{"model":"local-nemotron-4b","messages":[{"role":"user","content":"What is 2 + 2? Answer with one digit."}],"max_tokens":1,"stream":%t}`, stream)
				resp, err := request("POST", "/v1/chat/completions", body)
				if err != nil {
					return err
				}
				err = checkNativeCompletion(resp, stream)
				resp.Body.Close()
				if err != nil {
					return err
				}
				requests++
			}
			if err := expect("POST", "/internal/v1/backend/stop", 200); err != nil {
				return err
			}
			if manager.State() != "stopped" {
				return errors.New("native child not stopped/reaped")
			}
			if err := expect("GET", "/healthz", 200); err != nil {
				return err
			}
			if err := expect("GET", "/readyz", 503); err != nil {
				return err
			}
			if err := expect("GET", "/v1/models", 503); err != nil {
				return err
			}
			resp, err := request("POST", "/v1/chat/completions", `{"model":"local-nemotron-4b","messages":[{"role":"user","content":"hi"}]}`)
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != 503 {
				return errors.New("generation accepted while native backend stopped")
			}
			if cycle == 0 {
				if err := expect("POST", "/internal/v1/backend/start", 200); err != nil {
					return err
				}
				if manager.State() != "ready" {
					return errors.New("restarted native child not ready")
				}
				if err := expect("GET", "/readyz", 503); err != nil {
					return err
				}
				if err := expect("POST", "/internal/v1/admission/resume", 200); err != nil {
					return err
				}
				if err := expect("GET", "/readyz", 200); err != nil {
					return err
				}
			}
		}
		resp, err := request("GET", "/internal/v1/metrics", "")
		if err != nil {
			return err
		}
		var metrics struct {
			Samples, Completed int
			Tokens             uint64 `json:"completed_native_output_tokens"`
			NativeTTFT         struct {
				Samples int
				P50     *float64
				P95     *float64
			} `json:"native_ttft_ms"`
		}
		err = json.NewDecoder(resp.Body).Decode(&metrics)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || metrics.Samples != 4 || metrics.Completed != 4 || metrics.Tokens != 4 || metrics.NativeTTFT.Samples != 4 || metrics.NativeTTFT.P50 == nil || metrics.NativeTTFT.P95 != nil {
			return fmt.Errorf("native aggregate mismatch: %+v err=%v", metrics, err)
		}
		return nil
	}(ctx)
	if closeErr := manager.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	if requests != 4 {
		t.Fatalf("completed %d requests, want JSON and SSE before and after restart", requests)
	}
	entries, err := os.ReadDir(c.StateDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("state cleanup: entries=%v err=%v", entries, err)
	}
	t.Log("native authenticated stop/start/resume verified: JSON=4 and SSE=4 before/after reload; health alive while stopped; both children cleanly exited/reaped; state cleaned")
}

func checkNativeCompletion(resp *http.Response, stream bool) error {
	if resp.StatusCode != 200 {
		return fmt.Errorf("generation HTTP %d", resp.StatusCode)
	}
	type choice struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	}
	type completion struct {
		ID      string   `json:"id"`
		Choices []choice `json:"choices"`
	}
	if !stream {
		var v completion
		d := json.NewDecoder(io.LimitReader(resp.Body, 65536))
		if err := d.Decode(&v); err != nil {
			return err
		}
		if v.ID == "" || len(v.Choices) != 1 || strings.TrimSpace(v.Choices[0].Message.Content) != "4" || v.Choices[0].FinishReason == nil || *v.Choices[0].FinishReason != "length" {
			return errors.New("invalid native JSON completion")
		}
		return nil
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return errors.New("missing SSE content type")
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 65536))
	var text strings.Builder
	var id string
	finished, done := false, false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if done || !strings.HasPrefix(line, "data: ") {
			return errors.New("invalid SSE framing")
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			if !finished {
				return errors.New("SSE DONE before finish")
			}
			done = true
			continue
		}
		var v completion
		if err := json.Unmarshal([]byte(data), &v); err != nil {
			return err
		}
		if v.ID == "" || len(v.Choices) != 1 || finished {
			return errors.New("invalid native SSE completion")
		}
		if id == "" {
			id = v.ID
		}
		if id != v.ID {
			return errors.New("SSE ID changed")
		}
		text.WriteString(v.Choices[0].Delta.Content)
		if reason := v.Choices[0].FinishReason; reason != nil {
			if *reason != "length" {
				return errors.New("unexpected SSE finish reason")
			}
			finished = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !done || strings.TrimSpace(text.String()) != "4" {
		return errors.New("missing native SSE result or DONE")
	}
	return nil
}
