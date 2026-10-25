package service

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelConfigPreflightAndSave(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "model.bin")
	if err := os.WriteFile(model, []byte("GGUFfixture"), 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 32)
	s, err := New(Config{Listen: "127.0.0.1:8080", BackendGRPC: "127.0.0.1:1", AuthToken: token, ModelConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req := httptest.NewRequest("PUT", "/internal/v1/model-config", strings.NewReader(`{"path":"`+model+`","model_id":"local-test","batching":"queue","context_tokens":8192,"max_output_tokens":1024}`))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"preflight_level":"container_header_only"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for name, want := range map[string]string{"model.path": model + "\n", "model.id": "local-test\n", "batching": "queue\n", "context.tokens": "8192\n", "max-output.tokens": "1024\n"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s=%q err=%v", name, got, err)
		}
	}
	bad := httptest.NewRequest("PUT", "/internal/v1/model-config", strings.NewReader(`{"path":"relative","model_id":"bad id","batching":"many"}`))
	bad.Header.Set("Authorization", "Bearer "+token)
	bw := httptest.NewRecorder()
	s.Handler().ServeHTTP(bw, bad)
	if bw.Code != 422 {
		t.Fatalf("bad status=%d body=%s", bw.Code, bw.Body.String())
	}
}
