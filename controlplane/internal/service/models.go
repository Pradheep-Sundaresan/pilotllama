package service

import (
	"context"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "inferno.local/controlplane/gen/inferno/v1"
)

// A live alias observation, not a model artifact identity or a readiness lease.
func (s *Service) readyModel(ctx context.Context) (string, error) {
	backend, err := s.rpc.Status(ctx)
	if err != nil {
		return "", err
	}
	if backend.Response == nil {
		return "", status.Error(codes.DataLoss, "missing backend status")
	}
	if backend.Response.State != pb.BackendState_BACKEND_STATE_READY {
		return "", status.Error(codes.Unavailable, "backend not ready")
	}
	id := backend.Response.GetModelId()
	if id == "" {
		return "", status.Error(codes.DataLoss, "missing backend model alias")
	}
	return id, nil
}

type modelRecord struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created *int64 `json:"created"` // Artifact creation time is not supplied by gRPC.
	OwnedBy string `json:"owned_by"`
}

func (s *Service) models(w http.ResponseWriter, r *http.Request) {
	// Share the bounded public-operation capacity, without charging generation rate.
	select {
	case s.active <- struct{}{}:
		defer func() { <-s.active }()
	default:
		writeJSON(w, 429, fail("Public request capacity exceeded", "rate_limit_error", "public_capacity_exceeded"))
		return
	}
	id, err := s.readyModel(r.Context())
	if err != nil {
		code, body := mappedError(err)
		writeJSON(w, code, body)
		return
	}
	if requested := r.PathValue("model"); requested != "" && requested != id {
		writeJSON(w, 404, fail("Requested model is not loaded", "invalid_request_error", "model_not_found"))
		return
	}
	model := modelRecord{ID: id, Object: "model", OwnedBy: "inferno"}
	if r.PathValue("model") != "" {
		writeJSON(w, http.StatusOK, model)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Object string        `json:"object"`
		Data   []modelRecord `json:"data"`
	}{"list", []modelRecord{model}})
}
