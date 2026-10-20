package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type modelConfig struct {
	Path           string `json:"path"`
	FileName       string `json:"file_name"`
	ModelID        string `json:"model_id"`
	Batching       string `json:"batching"`
	SizeBytes      int64  `json:"size_bytes"`
	Format         string `json:"format"`
	PreflightLevel string `json:"preflight_level"`
	RestartNeeded  bool   `json:"restart_needed"`
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

func inspectModel(path, id, batching string) (modelConfig, error) {
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
	return modelConfig{path, filepath.Base(path), id, batching, info.Size(), "GGUF", "container_header_only", true}, nil
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
		cfg, err := inspectModel(path, id, batching)
		if err != nil {
			writeJSON(w, 422, fail(err.Error(), "invalid_request_error", "model_preflight_failed"))
			return
		}
		cfg.RestartNeeded = false
		writeJSON(w, 200, cfg)
		return
	}
	var input struct {
		Path     string `json:"path"`
		ModelID  string `json:"model_id"`
		Batching string `json:"batching"`
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
	cfg, err := inspectModel(input.Path, input.ModelID, input.Batching)
	if err != nil {
		writeJSON(w, 422, fail(err.Error(), "invalid_request_error", "model_preflight_failed"))
		return
	}
	for _, item := range []struct{ name, value string }{{"model.path", cfg.Path}, {"model.id", cfg.ModelID}, {"batching", cfg.Batching}} {
		if err := writePrivateAtomic(s.modelConfigDir, item.name, item.value); err != nil {
			writeJSON(w, 500, fail("Could not save model configuration", "server_error", "model_config_write_failed"))
			return
		}
	}
	writeJSON(w, 200, cfg)
}
