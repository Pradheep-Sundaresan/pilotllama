package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type modelConfig struct {
	Path            string `json:"path"`
	FileName        string `json:"file_name"`
	DraftModelPath  string `json:"draft_model_path"`
	DraftFileName   string `json:"draft_file_name"`
	ModelID         string `json:"model_id"`
	Batching        string `json:"batching"`
	ContextTokens   uint32 `json:"context_tokens"`
	MaxOutputTokens uint32 `json:"max_output_tokens"`
	SizeBytes       int64  `json:"size_bytes"`
	DraftSizeBytes  int64  `json:"draft_size_bytes"`
	Format          string `json:"format"`
	PreflightLevel  string `json:"preflight_level"`
	RestartNeeded   bool   `json:"restart_needed"`
}

func readSmallFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	if len(b) > 4096 {
		return "", errors.New("configuration value is too large")
	}
	return strings.TrimSuffix(string(b), "\n"), nil
}

func inspectModel(path, draft, id, batching string, contextTokens, maxOutputTokens uint32) (modelConfig, error) {
	if !filepath.IsAbs(path) {
		return modelConfig{}, errors.New("model path must be absolute")
	}
	if id == "" || len(id) > 128 || strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-')
	}) >= 0 {
		return modelConfig{}, errors.New("model ID must use 1..128 letters, digits, dot, underscore or hyphen")
	}
	if batching != "queue" && batching != "continuous" {
		return modelConfig{}, errors.New("batching must be queue or continuous")
	}
	if contextTokens < 32 || contextTokens > 1048576 || maxOutputTokens == 0 || maxOutputTokens > contextTokens {
		return modelConfig{}, errors.New("context must be 32..1048576 tokens and output must fit inside it")
	}
	if batching == "continuous" && (contextTokens < 64 || maxOutputTokens > contextTokens/2) {
		return modelConfig{}, errors.New("continuous output budget must fit each half of the shared context")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return modelConfig{}, errors.New("model file is not accessible")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return modelConfig{}, errors.New("model path must be a regular file, not a symlink")
	}
	f, err := os.Open(path)
	if err != nil {
		return modelConfig{}, errors.New("model file is not readable")
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != "GGUF" {
		return modelConfig{}, errors.New("file does not have a GGUF container header")
	}
	draftName, draftSize := "", int64(0)
	if draft != "" {
		draftInfo, err := os.Lstat(draft)
		if !filepath.IsAbs(draft) || err != nil || draftInfo.Mode()&os.ModeSymlink != 0 || !draftInfo.Mode().IsRegular() {
			return modelConfig{}, errors.New("draft model must be an absolute regular non-symlink file")
		}
		draftFile, err := os.Open(draft)
		if err != nil {
			return modelConfig{}, errors.New("draft model is not readable")
		}
		defer draftFile.Close()
		var draftMagic [4]byte
		if _, err := io.ReadFull(draftFile, draftMagic[:]); err != nil || string(draftMagic[:]) != "GGUF" {
			return modelConfig{}, errors.New("draft model does not have a GGUF container header")
		}
		draftName, draftSize = filepath.Base(draft), draftInfo.Size()
	}
	return modelConfig{path, filepath.Base(path), draft, draftName, id, batching, contextTokens, maxOutputTokens,
		info.Size(), draftSize, "GGUF", "container_header_only", true}, nil
}

func readBudget(dir, name string, fallback uint32) uint32 {
	value, err := readSmallFile(filepath.Join(dir, name))
	if err != nil {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return fallback
	}
	return uint32(parsed)
}

func writePrivateAtomic(dir, name, value string) error {
	f, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.WriteString(value + "\n")
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

func (s *Service) modelConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		path, e1 := readSmallFile(filepath.Join(s.modelConfigDir, "model.path"))
		id, e2 := readSmallFile(filepath.Join(s.modelConfigDir, "model.id"))
		batching, e3 := readSmallFile(filepath.Join(s.modelConfigDir, "batching"))
		if e1 != nil || e2 != nil || e3 != nil {
			writeJSON(w, 404, fail("No saved model configuration", "invalid_request_error", "model_config_not_found"))
			return
		}
		draft, _ := readSmallFile(filepath.Join(s.modelConfigDir, "draft-model.path"))
		cfg, err := inspectModel(path, draft, id, batching,
			readBudget(s.modelConfigDir, "context.tokens", 4096), readBudget(s.modelConfigDir, "max-output.tokens", 512))
		if err != nil {
			writeJSON(w, 422, fail(err.Error(), "invalid_request_error", "model_preflight_failed"))
			return
		}
		cfg.RestartNeeded = false
		writeJSON(w, 200, cfg)
		return
	}
	var input struct {
		Path            string `json:"path"`
		DraftModelPath  string `json:"draft_model_path"`
		ModelID         string `json:"model_id"`
		Batching        string `json:"batching"`
		ContextTokens   uint32 `json:"context_tokens"`
		MaxOutputTokens uint32 `json:"max_output_tokens"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 16*1024+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		writeJSON(w, 400, fail("Invalid model configuration", "invalid_request_error", "invalid_json"))
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, 400, fail("Invalid model configuration", "invalid_request_error", "invalid_json"))
		return
	}
	if input.ContextTokens == 0 {
		input.ContextTokens = 4096
	}
	if input.MaxOutputTokens == 0 {
		input.MaxOutputTokens = 512
	}
	cfg, err := inspectModel(input.Path, input.DraftModelPath, input.ModelID, input.Batching, input.ContextTokens, input.MaxOutputTokens)
	if err != nil {
		writeJSON(w, 422, fail(err.Error(), "invalid_request_error", "model_preflight_failed"))
		return
	}
	for _, item := range []struct{ name, value string }{{"model.path", cfg.Path}, {"model.id", cfg.ModelID}, {"batching", cfg.Batching},
		{"draft-model.path", cfg.DraftModelPath},
		{"context.tokens", strconv.FormatUint(uint64(cfg.ContextTokens), 10)},
		{"max-output.tokens", strconv.FormatUint(uint64(cfg.MaxOutputTokens), 10)}} {
		if err := writePrivateAtomic(s.modelConfigDir, item.name, item.value); err != nil {
			writeJSON(w, 500, fail("Could not save model configuration", "server_error", "model_config_write_failed"))
			return
		}
	}
	writeJSON(w, 200, cfg)
}
