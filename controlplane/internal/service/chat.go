package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

const bodyLimit = 128 * 1024
const outputLimit = 1024 * 1024

var errText = errors.New("invalid backend UTF-8")
var errOutput = errors.New("output exceeds HTTP byte limit")

type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Stream              bool     `json:"stream"`
	MaxTokens           *int     `json:"max_tokens"`
	MaxCompletionTokens *int     `json:"max_completion_tokens"`
	Temperature         *float64 `json:"temperature"`
	N                   *int     `json:"n"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    string `json:"code"`
}
type errorEnvelope struct {
	Error apiError `json:"error"`
}

func fail(message, kind, code string) errorEnvelope {
	return errorEnvelope{apiError{message, kind, nil, code}}
}

func setWriteDeadline(w http.ResponseWriter) error {
	err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
	// In-memory test recorders have no socket deadline. Run's real HTTP writer does.
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

type textDecoder struct {
	pending []byte
	total   int
}

func (d *textDecoder) push(piece []byte) (string, error) {
	d.total += len(piece)
	if d.total > outputLimit {
		return "", errOutput
	}
	d.pending = append(d.pending, piece...)
	end := 0
	for end < len(d.pending) {
		if !utf8.FullRune(d.pending[end:]) {
			break
		}
		r, n := utf8.DecodeRune(d.pending[end:])
		if r == utf8.RuneError && n == 1 {
			return "", errText
		}
		end += n
	}
	text := string(d.pending[:end])
	d.pending = append(d.pending[:0], d.pending[end:]...)
	return text, nil
}

func mappedError(err error) (int, errorEnvelope) {
	if errors.Is(err, errText) {
		return 502, fail("Backend returned invalid text", "server_error", "invalid_backend_text")
	}
	if errors.Is(err, errOutput) {
		return 502, fail("Generated output exceeds the byte limit", "server_error", "response_too_large")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return 504, fail("Request deadline exceeded", "server_error", "request_timeout")
	}
	if errors.Is(err, context.Canceled) {
		return 408, fail("Request cancelled", "server_error", "request_cancelled")
	}
	switch status.Code(err) {
	case codes.InvalidArgument:
		return 400, fail("Invalid generation request", "invalid_request_error", "invalid_request")
	case codes.ResourceExhausted:
		return 429, fail("Server is busy; retry later", "server_error", "server_busy")
	case codes.Unavailable:
		return 503, fail("Inference backend unavailable", "server_error", "backend_unavailable")
	case codes.DeadlineExceeded:
		return 504, fail("Request deadline exceeded", "server_error", "request_timeout")
	case codes.Canceled:
		return 408, fail("Request cancelled", "server_error", "request_cancelled")
	default:
		return 502, fail("Inference request failed", "server_error", "backend_error")
	}
}

func chunk(id, model string, created int64, delta map[string]string, reason any) map[string]any {
	return map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "logprobs": nil, "finish_reason": reason}}}
}

func (s *Service) chat(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	var observation *rpcbackend.Observation
	var streamFailure *bool
	outcome := "failed"
	id := s.ids.Add(1)
	defer func() {
		if observation != nil {
			if streamFailure != nil && *streamFailure {
				outcome = "delivery_failed"
			}
			s.history.add(makeRecord(id, startedAt, outcome, *observation, streamFailure))
		}
	}()
	if id == 0 {
		writeJSON(w, 503, fail("Request ID space exhausted", "server_error", "server_unavailable"))
		return
	}
	w.Header().Set("X-Request-ID", strconv.FormatUint(id, 10))
	select {
	case s.active <- struct{}{}:
		defer func() { <-s.active }()
	default:
		writeJSON(w, 429, fail("Server is busy; retry later", "server_error", "server_busy"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.requestTimeout)
	defer cancel()
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeJSON(w, 415, fail("Use Content-Type application/json", "invalid_request_error", "unsupported_media_type"))
		return
	}
	readDeadline := time.Now().Add(5 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(readDeadline) {
		readDeadline = deadline
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(readDeadline)
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
	// Body input is complete: don't let its deadline cancel a long prefill or
	// the connection's background disconnect reader during response streaming.
	_ = controller.SetReadDeadline(time.Time{})
	if err != nil {
		if ctx.Err() != nil {
			code, body := mappedError(ctx.Err())
			writeJSON(w, code, body)
			return
		}
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			writeJSON(w, 413, fail("Request body too large", "invalid_request_error", "request_too_large"))
		} else {
			writeJSON(w, 400, fail("Cannot read request body", "invalid_request_error", "invalid_request"))
		}
		return
	}
	if !utf8.Valid(data) {
		writeJSON(w, 400, fail("Request must be UTF-8 JSON", "invalid_request_error", "invalid_request"))
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input chatRequest
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeJSON(w, 400, fail("Invalid or unsupported request fields", "invalid_request_error", "invalid_request"))
		return
	}
	limit := 128
	if input.MaxTokens != nil {
		limit = *input.MaxTokens
	}
	if input.MaxCompletionTokens != nil {
		limit = *input.MaxCompletionTokens
	}
	if input.Model == "" || len(input.Model) > 256 || len(input.Messages) != 1 || input.Messages[0].Role != "user" ||
		(input.MaxTokens != nil && input.MaxCompletionTokens != nil) || limit < 1 || limit > 512 ||
		(input.Temperature != nil && *input.Temperature != 0) || (input.N != nil && *input.N != 1) {
		writeJSON(w, 400, fail("Require one user message, token limit 1..512, temperature 0 and n 1", "invalid_request_error", "unsupported_request"))
		return
	}
	prompt := input.Messages[0].Content
	if len(prompt) > 65536 || strings.Contains(prompt, "<|im_start|>") || strings.Contains(prompt, "<|im_end|>") {
		writeJSON(w, 400, fail("Invalid prompt", "invalid_request_error", "invalid_prompt"))
		return
	}
	modelID, err := s.readyModel(ctx)
	if err != nil {
		code, body := mappedError(err)
		writeJSON(w, code, body)
		return
	}
	if modelID != input.Model {
		writeJSON(w, 404, fail("Requested model is not loaded", "invalid_request_error", "model_not_found"))
		return
	}
	completionID := "chatcmpl-" + strconv.FormatUint(id, 10)
	created := time.Now().Unix()
	started := false
	deliveryFailed := false
	if input.Stream {
		streamFailure = &deliveryFailed
	}
	send := func(value any) error {
		if err := setWriteDeadline(w); err != nil {
			deliveryFailed = true
			return err
		}
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(200)
			started = true
		}
		var data []byte
		if marker, ok := value.(string); ok {
			data = []byte(marker)
		} else {
			var err error
			data, err = json.Marshal(value)
			if err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			deliveryFailed = true
			return err
		}
		err := http.NewResponseController(w).Flush()
		if err != nil {
			deliveryFailed = true
		}
		return err
	}
	begin := func() error {
		if !started {
			return send(chunk(completionID, input.Model, created, map[string]string{"role": "assistant", "content": ""}, nil))
		}
		return nil
	}
	var text strings.Builder
	var utf textDecoder
	result, err := s.rpc.Generate(ctx, &pb.GenerateRequest{RequestId: id, Prompt: prompt, MaxOutputTokens: uint32(limit)}, func(piece []byte) error {
		valid, err := utf.push(piece)
		if err != nil {
			return err
		}
		if valid == "" {
			return nil
		}
		if input.Stream {
			if err := begin(); err != nil {
				return err
			}
			return send(chunk(completionID, input.Model, created, map[string]string{"content": valid}, nil))
		}
		_, err = text.WriteString(valid)
		return err
	})
	observation = &result
	if err == nil && len(utf.pending) != 0 {
		err = errText
	}
	if err == nil && (!result.TransportComplete || result.Finished == nil) {
		err = status.Error(codes.DataLoss, "Incomplete inference response")
	}
	if err == nil {
		switch result.Finished.State {
		case pb.TerminalState_TERMINAL_STATE_COMPLETED:
		case pb.TerminalState_TERMINAL_STATE_CANCELLED:
			err = status.Error(codes.Canceled, "Native cancellation")
		case pb.TerminalState_TERMINAL_STATE_TIMED_OUT:
			err = status.Error(codes.DeadlineExceeded, "Native deadline")
		default:
			err = status.Error(codes.Internal, "Native failure")
		}
	}
	if err != nil {
		if deliveryFailed {
			return
		}
		code, body := mappedError(err)
		if started {
			_ = send(body)
		} else {
			writeJSON(w, code, body)
		}
		return // No success finish or [DONE] on failure; already sent text is partial.
	}
	reason := "stop"
	outcome = "completed"
	if result.Finished.Metrics.OutputTokens >= uint32(limit) {
		reason = "length"
	}
	if input.Stream {
		if begin() != nil {
			return
		}
		if send(chunk(completionID, input.Model, created, map[string]string{}, reason)) != nil {
			return
		}
		_ = send("[DONE]")
		return
	}
	writeJSON(w, 200, map[string]any{"id": completionID, "object": "chat.completion", "created": created, "model": input.Model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": text.String()}, "logprobs": nil, "finish_reason": reason}}})
}
