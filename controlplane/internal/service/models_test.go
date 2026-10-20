package service

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "inferno.local/controlplane/gen/inferno/v1"
)

func TestModelsDiscoveryAndGenerationAlias(t *testing.T) {
	s, b := authenticatedService(t, testSecret)
	get := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := get("/v1/models", ""); w.Code != 401 || b.statusCalls != 0 {
		t.Fatal("discovery bypassed auth")
	}
	// Catalog reads neither spend nor depend on generation rate credit.
	s.limiter = newRequestLimiter(1, 1, time.Now())
	s.limiter.take(time.Now())
	w := get("/v1/models", testSecret)
	var list struct {
		Object string
		Data   []modelRecord
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || list.Object != "list" || len(list.Data) != 1 || list.Data[0].ID != "test-model" || list.Data[0].Object != "model" || list.Data[0].Created != nil || list.Data[0].OwnedBy != "inferno" {
		t.Fatalf("catalog: %d %s", w.Code, w.Body)
	}
	w = get("/v1/models/test-model", testSecret)
	var model modelRecord
	if err := json.Unmarshal(w.Body.Bytes(), &model); err != nil || model != list.Data[0] {
		t.Fatal("detail/list mismatch", err)
	}
	if w := get("/v1/models/unknown", testSecret); w.Code != 404 || !strings.Contains(w.Body.String(), "model_not_found") {
		t.Fatal("unknown alias")
	}
	for _, state := range []pb.BackendState{pb.BackendState_BACKEND_STATE_NO_MODEL, pb.BackendState_BACKEND_STATE_LOADING, pb.BackendState_BACKEND_STATE_DRAINING, pb.BackendState_BACKEND_STATE_FAILED} {
		b.state = state
		if w := get("/v1/models", testSecret); w.Code != 503 {
			t.Fatalf("non-ready state %v: %d", state, w.Code)
		}
		s.limiter = nil
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body(false)))
		r.Header.Set("Authorization", "Bearer "+testSecret)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatal("generation readiness inconsistent")
		}
	}
	if b.generationCalls != 0 {
		t.Fatal("discovery triggered generation")
	}
	b.state = pb.BackendState_BACKEND_STATE_READY
	for i := 0; i < cap(s.active); i++ {
		s.active <- struct{}{}
	}
	if w := get("/v1/models", testSecret); w.Code != 429 {
		t.Fatal("catalog capacity unbounded")
	}
	for len(s.active) > 0 {
		<-s.active
	}
}
