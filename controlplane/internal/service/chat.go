package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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

const bodyLimit = 1024*1024 + 64*1024
const outputLimit = 1024 * 1024

var errText = errors.New("invalid backend UTF-8")
var errOutput = errors.New("output exceeds HTTP byte limit")

type chatRequest struct {
	Model               string               `json:"model"`
	Messages            []chatMessageInput   `json:"messages"`
	Stream              bool                 `json:"stream"`
	MaxTokens           *int                 `json:"max_tokens"`
	MaxCompletionTokens *int                 `json:"max_completion_tokens"`
	Temperature         *float64             `json:"temperature"`
	TopP                *float64             `json:"top_p"`
	TopK                *int                 `json:"top_k"`
	Seed                *int64               `json:"seed"`
	Stop                stopInput            `json:"stop"`
	N                   *int                 `json:"n"`
	Thinking            bool                 `json:"thinking"`
	Tools               []toolInput          `json:"tools"`
	ToolChoice          *string              `json:"tool_choice"`
	ResponseFormat      *responseFormatInput `json:"response_format"`
}

type responseFormatInput struct {
	Type       string `json:"type"`
	JSONSchema *struct {
		Name   string          `json:"name"`
		Strict *bool           `json:"strict"`
		Schema json.RawMessage `json:"schema"`
	} `json:"json_schema"`
}

type toolInput struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func validToolName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for _, c := range []byte(name) {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func validateTools(tools []toolInput, choice *string) ([]*pb.ToolDefinition, pb.ToolChoice, bool) {
	selected := pb.ToolChoice_TOOL_CHOICE_NONE
	if len(tools) > 0 {
		selected = pb.ToolChoice_TOOL_CHOICE_AUTO
	}
	if choice != nil {
		switch *choice {
		case "none":
			selected = pb.ToolChoice_TOOL_CHOICE_NONE
		case "auto":
			selected = pb.ToolChoice_TOOL_CHOICE_AUTO
		case "required":
			selected = pb.ToolChoice_TOOL_CHOICE_REQUIRED
		default:
			return nil, 0, false
		}
	}
	if len(tools) == 0 {
		return nil, selected, selected == pb.ToolChoice_TOOL_CHOICE_NONE
	}
	if len(tools) > 16 {
		return nil, 0, false
	}
	result, names, total := make([]*pb.ToolDefinition, 0, len(tools)), map[string]bool{}, 0
	for _, tool := range tools {
		var object map[string]any
		if tool.Type != "function" || !validToolName(tool.Function.Name) || names[tool.Function.Name] ||
			len(tool.Function.Description) > 1024 || !utf8.ValidString(tool.Function.Description) ||
			len(tool.Function.Parameters) == 0 || len(tool.Function.Parameters) > 16384 ||
			total > 65536-len(tool.Function.Parameters) || json.Unmarshal(tool.Function.Parameters, &object) != nil || object == nil {
			return nil, 0, false
		}
		names[tool.Function.Name] = true
		total += len(tool.Function.Parameters)
		result = append(result, &pb.ToolDefinition{Name: tool.Function.Name, Description: tool.Function.Description, ParametersJson: string(tool.Function.Parameters)})
	}
	return result, selected, true
}

type stopInput []string

func (s *stopInput) UnmarshalJSON(data []byte) error {
	var one string
	if json.Unmarshal(data, &one) == nil {
		*s = []string{one}
		return nil
	}
	var many []string
	if json.Unmarshal(data, &many) != nil {
		return errors.New("stop must be a string or string array")
	}
	*s = many
	return nil
}

func validStops(stops []string) bool {
	if len(stops) > 4 {
		return false
	}
	total := 0
	for _, stop := range stops {
		if stop == "" || len(stop) > 256 || !utf8.ValidString(stop) || total > 512-len(stop) {
			return false
		}
		total += len(stop)
	}
	return true
}

type chatMessageInput struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	ToolCalls []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
	ToolCallID string `json:"tool_call_id"`
}

func validateChatMessages(messages []chatMessageInput) ([]*pb.ChatMessage, bool) {
	if len(messages) < 1 || len(messages) > 64 {
		return nil, false
	}
	total, expected, pending := 0, "user", map[string]bool{}
	result := make([]*pb.ChatMessage, 0, len(messages))
	for index, message := range messages {
		if len(message.Content) > 1024*1024-total || strings.Contains(message.Content, "<|im_start|>") || strings.Contains(message.Content, "<|im_end|>") {
			return nil, false
		}
		total += len(message.Content)
		wire := &pb.ChatMessage{Content: message.Content, ToolCallId: message.ToolCallID}
		switch message.Role {
		case "system":
			if index != 0 || len(message.ToolCalls) != 0 || message.ToolCallID != "" {
				return nil, false
			}
			wire.Role = pb.ChatRole_CHAT_ROLE_SYSTEM
			result = append(result, wire)
			continue
		case "user":
			if expected != "user" || len(message.ToolCalls) != 0 || message.ToolCallID != "" {
				return nil, false
			}
			wire.Role, expected = pb.ChatRole_CHAT_ROLE_USER, "assistant"
		case "assistant":
			if (expected != "assistant" && expected != "assistant_after_tools") || message.ToolCallID != "" || len(message.ToolCalls) > 16 {
				return nil, false
			}
			wire.Role, pending = pb.ChatRole_CHAT_ROLE_ASSISTANT, map[string]bool{}
			for _, call := range message.ToolCalls {
				var args map[string]any
				if len(call.ID) < 1 || len(call.ID) > 128 || call.Type != "function" || !validToolName(call.Function.Name) || len(call.Function.Arguments) > 65536 || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil || pending[call.ID] {
					return nil, false
				}
				total += len(call.Function.Arguments)
				if total > 1024*1024 {
					return nil, false
				}
				pending[call.ID] = true
				wire.ToolCalls = append(wire.ToolCalls, &pb.ToolCall{Id: call.ID, Name: call.Function.Name, ArgumentsJson: call.Function.Arguments})
			}
			if len(pending) == 0 {
				expected = "user"
			} else {
				expected = "tools"
			}
		case "tool":
			if expected != "tools" || len(message.ToolCalls) != 0 || !pending[message.ToolCallID] {
				return nil, false
			}
			wire.Role = pb.ChatRole_CHAT_ROLE_TOOL
			delete(pending, message.ToolCallID)
			if len(pending) == 0 {
				expected = "assistant_after_tools"
			}
		default:
			return nil, false
		}
		result = append(result, wire)
	}
	return result, expected == "assistant" || expected == "assistant_after_tools"
}

func validateResponseFormat(input *responseFormatInput) (pb.ResponseFormat, string, bool) {
	if input == nil || input.Type == "text" {
		return pb.ResponseFormat_RESPONSE_FORMAT_TEXT, "", input == nil || input.JSONSchema == nil
	}
	if input.Type == "json_object" {
		return pb.ResponseFormat_RESPONSE_FORMAT_JSON_OBJECT, `{"type":"object"}`, input.JSONSchema == nil
	}
	if input.Type != "json_schema" || input.JSONSchema == nil || !validToolName(input.JSONSchema.Name) || len(input.JSONSchema.Schema) == 0 || len(input.JSONSchema.Schema) > 65536 {
		return 0, "", false
	}
	var object map[string]any
	if json.Unmarshal(input.JSONSchema.Schema, &object) != nil || object == nil {
		return 0, "", false
	}
	return pb.ResponseFormat_RESPONSE_FORMAT_JSON_SCHEMA, string(input.JSONSchema.Schema), true
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
}

func (d *textDecoder) push(piece []byte) (string, error) {
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

func chunk(id, model string, created int64, delta any, reason any) map[string]any {
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
	messages, validMessages := validateChatMessages(input.Messages)
	tools, toolChoice, validTools := validateTools(input.Tools, input.ToolChoice)
	responseFormat, responseSchema, validResponseFormat := validateResponseFormat(input.ResponseFormat)
	temperature, topP, topK := 0.0, 1.0, 0
	if input.Temperature != nil {
		temperature = *input.Temperature
	}
	if input.TopP != nil {
		topP = *input.TopP
	}
	if input.TopK != nil {
		topK = *input.TopK
	}
	if input.Model == "" || len(input.Model) > 256 || !validMessages || !validTools || !validResponseFormat ||
		(len(tools) > 0 && responseFormat != pb.ResponseFormat_RESPONSE_FORMAT_TEXT) ||
		(input.MaxTokens != nil && input.MaxCompletionTokens != nil) || limit < 1 || limit > s.maxOutputTokens ||
		math.IsNaN(temperature) || math.IsInf(temperature, 0) || temperature < 0 || temperature > 2 ||
		math.IsNaN(topP) || math.IsInf(topP, 0) || topP <= 0 || topP > 1 || topK < 0 || topK > 100000 ||
		(input.Seed != nil && (*input.Seed < 0 || *input.Seed > math.MaxUint32)) || !validStops(input.Stop) ||
		(input.N != nil && *input.N != 1) {
		writeJSON(w, 400, fail(fmt.Sprintf("Require valid chat, token limit 1..%d, temperature 0..2, top_p (0,1], top_k 0..100000, uint32 seed, at most 4 stop strings, and n 1", s.maxOutputTokens), "invalid_request_error", "unsupported_request"))
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
	type toolCallOutput struct {
		id, name             string
		arguments            strings.Builder
		decoder              textDecoder
		started, syntheticID bool
	}
	var text, reasoning strings.Builder
	toolCalls := []*toolCallOutput{}
	var contentUTF, reasoningUTF textDecoder
	outputBytes := 0
	rpcRequest := &pb.GenerateRequest{RequestId: id, Messages: messages, MaxOutputTokens: uint32(limit), EnableThinking: input.Thinking,
		Temperature: float32(temperature), TopP: float32(topP), TopK: int32(topK), Stop: []string(input.Stop), Tools: tools, ToolChoice: toolChoice,
		ResponseFormat: responseFormat, ResponseSchemaJson: responseSchema}
	if input.Seed != nil {
		value := uint32(*input.Seed)
		rpcRequest.Seed = &value
	}
	result, err := s.rpc.GeneratePieces(ctx, rpcRequest, func(piece rpcbackend.OutputPiece) error {
		outputBytes += len(piece.Bytes)
		if outputBytes > outputLimit {
			return errOutput
		}
		if piece.Channel == pb.TokenChannel_TOKEN_CHANNEL_TOOL_CALL {
			for len(toolCalls) <= int(piece.ToolCallIndex) {
				toolCalls = append(toolCalls, &toolCallOutput{})
			}
			call := toolCalls[piece.ToolCallIndex]
			first := !call.started
			if first {
				call.id, call.started = piece.ToolCallID, true
				if call.id == "" {
					call.id, call.syntheticID = fmt.Sprintf("call_%d_%d", id, piece.ToolCallIndex), true
				}
			}
			idDelta, nameDelta := "", ""
			if first {
				idDelta = call.id
			} else if piece.ToolCallID != "" && piece.ToolCallID != call.id && !call.syntheticID {
				return errors.New("tool-call ID changed")
			}
			if piece.ToolName != "" {
				if strings.HasPrefix(piece.ToolName, call.name) {
					nameDelta = strings.TrimPrefix(piece.ToolName, call.name)
				}
				call.name = piece.ToolName
			}
			arguments, err := call.decoder.push(piece.Bytes)
			if err != nil {
				return err
			}
			_, _ = call.arguments.WriteString(arguments)
			if input.Stream && (first || idDelta != "" || nameDelta != "" || arguments != "") {
				if err := begin(); err != nil {
					return err
				}
				function := map[string]string{}
				if nameDelta != "" {
					function["name"] = nameDelta
				}
				if arguments != "" {
					function["arguments"] = arguments
				}
				entry := map[string]any{"index": piece.ToolCallIndex, "function": function}
				if idDelta != "" {
					entry["id"] = idDelta
					entry["type"] = "function"
				}
				return send(chunk(completionID, input.Model, created, map[string]any{"tool_calls": []any{entry}}, nil))
			}
			return nil
		}
		decoder := &contentUTF
		field := "content"
		builder := &text
		if piece.Channel == pb.TokenChannel_TOKEN_CHANNEL_REASONING {
			decoder = &reasoningUTF
			field = "reasoning_content"
			builder = &reasoning
		}
		valid, err := decoder.push(piece.Bytes)
		if err != nil {
			return err
		}
		if valid == "" {
			return nil
		}
		_, _ = builder.WriteString(valid)
		if input.Stream {
			if err := begin(); err != nil {
				return err
			}
			return send(chunk(completionID, input.Model, created, map[string]string{field: valid}, nil))
		}
		return nil
	})
	observation = &result
	if err == nil && (len(contentUTF.pending) != 0 || len(reasoningUTF.pending) != 0) {
		err = errText
	}
	if err == nil {
		for _, call := range toolCalls {
			if len(call.decoder.pending) != 0 {
				err = errText
				break
			}
		}
	}
	if err == nil && (!result.TransportComplete || result.Finished == nil) {
		err = status.Error(codes.DataLoss, "Incomplete inference response")
	}
	if err == nil && responseFormat != pb.ResponseFormat_RESPONSE_FORMAT_TEXT {
		var object map[string]any
		if json.Unmarshal([]byte(text.String()), &object) != nil || object == nil {
			err = errors.New("backend returned invalid structured output")
		}
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
	} else if len(toolCalls) > 0 {
		reason = "tool_calls"
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
	message := map[string]any{"role": "assistant", "content": text.String()}
	if input.Thinking {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolCalls) > 0 {
		calls := make([]any, 0, len(toolCalls))
		for _, call := range toolCalls {
			calls = append(calls, map[string]any{"id": call.id, "type": "function", "function": map[string]string{"name": call.name, "arguments": call.arguments.String()}})
		}
		message["tool_calls"] = calls
	}
	writeJSON(w, 200, map[string]any{"id": completionID, "object": "chat.completion", "created": created, "model": input.Model,
		"choices": []any{map[string]any{"index": 0, "message": message, "logprobs": nil, "finish_reason": reason}}})
}
