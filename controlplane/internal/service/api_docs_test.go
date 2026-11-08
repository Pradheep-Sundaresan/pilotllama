package service

import (
	"os"
	"strings"
	"testing"
)

// This intentionally checks only contract anchors. Runtime shape and ordering are
// covered by chat/models/auth/queue tests; these anchors keep the public guide and
// machine-readable entry points from silently disappearing.
func TestPublishedHTTPContractAnchors(t *testing.T) {
	files := map[string][]string{
		"../../../docs/http-api.md": {
			"GET /v1/models", "POST /v1/chat/completions", "pilotllama.metrics",
			"queue_full", "queue_wait_timeout", "host.docker.internal",
		},
		"../../../docs/api-compatibility.md": {
			"Standard stream `usage`", "Measured stream telemetry", "Embeddings",
		},
		"../../../api/openapi.yaml": {
			"/healthz:", "/readyz:", "/v1/models:", "/v1/chat/completions:",
			"include_metrics:",
		},
		"../../../README.md": {
			"[HTTP API guide](docs/http-api.md)", "[OpenAPI specification](api/openapi.yaml)",
		},
	}
	for name, anchors := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, anchor := range anchors {
			if !strings.Contains(string(data), anchor) {
				t.Errorf("%s missing contract anchor %q", name, anchor)
			}
		}
	}
}
