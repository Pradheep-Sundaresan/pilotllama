package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

const testSecret = "local-test-secret-0123456789abcdef"

type authBackend struct {
	chatBackend
	statusCalls, generationCalls int
}

func (b *authBackend) Status(ctx context.Context) (rpcbackend.StatusObservation, error) {
	b.statusCalls++
	return b.chatBackend.Status(ctx)
}
func (b *authBackend) Generate(_ context.Context, _ *pb.GenerateRequest, consume func([]byte) error) (rpcbackend.Observation, error) {
	b.generationCalls++
	if err := consume([]byte("ok")); err != nil {
		return rpcbackend.Observation{}, err
	}
	return success(1), nil
}
func (b *authBackend) GeneratePieces(ctx context.Context, request *pb.GenerateRequest, consume func(rpcbackend.OutputPiece) error) (rpcbackend.Observation, error) {
	return b.Generate(ctx, request, func(piece []byte) error {
		return consume(rpcbackend.OutputPiece{Bytes: piece, Channel: pb.TokenChannel_TOKEN_CHANNEL_CONTENT, AdvancesOutputTokens: true})
	})
}

type unreadBody struct{ t *testing.T }

func (b unreadBody) Read([]byte) (int, error) { b.t.Error("unauthorized body read"); return 0, io.EOF }
func (unreadBody) Close() error               { return nil }

func authenticatedService(t *testing.T, token string) (*Service, *authBackend) {
	t.Helper()
	s, err := New(Config{Listen: "127.0.0.1:8080", BackendGRPC: "127.0.0.1:8081", AuthToken: token})
	if err != nil {
		t.Fatal(err)
	}
	s.Close() // Replace the real client with a backend that records any work.
	b := &authBackend{}
	s.rpc = b
	return s, b
}

func TestAuthRejectsBeforeAnyWork(t *testing.T) {
	s, b := authenticatedService(t, testSecret)
	initialID := s.ids.Load()
	for _, headers := range [][]string{
		nil, {""}, {"Bearer"}, {"Basic " + testSecret}, {"Bearer wrong"},
		{"Bearer " + strings.Repeat("x", 32)}, {"Bearer  " + testSecret},
		{"Bearer " + testSecret + " "}, {"Bearer\t" + testSecret},
		{"Bearer " + testSecret, "Bearer " + testSecret},
		{"Bearer " + testSecret + ", Bearer " + testSecret},
		{"Bearer " + strings.Repeat("x", 257)},
	} {
		r := httptest.NewRequest("POST", "/v1/chat/completions?api_key="+testSecret, nil)
		r.Body = unreadBody{t}
		for _, value := range headers {
			r.Header.Add("Authorization", value)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") != `Bearer realm="inferno"` || !strings.Contains(w.Body.String(), `"authentication_error"`) {
			t.Fatalf("unexpected auth rejection: %d %s", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), testSecret) || strings.Contains(w.Header().Get("WWW-Authenticate"), testSecret) {
			t.Fatal("credential reflected")
		}
	}
	if b.statusCalls != 0 || b.generationCalls != 0 || len(s.active) != 0 || s.ids.Load() != initialID {
		t.Fatalf("unauthorized request reached backend or admission: %+v", b)
	}
}

func TestAuthValidJSONAndSSEAndPublicHealth(t *testing.T) {
	s, b := authenticatedService(t, testSecret)
	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		for _, stream := range []bool{false, true} {
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body(stream)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", scheme+" "+testSecret)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "ok") || strings.Contains(w.Body.String(), testSecret) {
				t.Fatalf("valid auth: %d %s", w.Code, w.Body)
			}
			if stream && !strings.Contains(w.Body.String(), "[DONE]") {
				t.Fatal("missing SSE completion")
			}
		}
	}
	if b.generationCalls != 6 {
		t.Fatal(b.generationCalls)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("public %s: %d", path, w.Code)
		}
	}
	// Auth stays optional for existing local clients.
	s, b = authenticatedService(t, "")
	if w := invoke(s, body(false)); w.Code != http.StatusOK || b.generationCalls != 1 {
		t.Fatal("disabled auth changed local behavior")
	}
}

func TestAuthConfiguration(t *testing.T) {
	for _, token := range []string{"short", strings.Repeat("a", 257), strings.Repeat("a", 32) + "\n", strings.Repeat("=", 32), "a=" + strings.Repeat("b", 32)} {
		_, err := New(Config{Listen: "127.0.0.1:8080", BackendGRPC: "127.0.0.1:8081", AuthToken: token})
		if err == nil || strings.Contains(err.Error(), token) {
			t.Fatal("invalid auth config accepted or secret exposed")
		}
	}
}
