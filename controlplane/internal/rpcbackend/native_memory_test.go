package rpcbackend_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	pb "inferno.local/controlplane/gen/inferno/v1"
)

type memoryBaselineObservation struct {
	Stage              string          `json:"stage"`
	StatusRPCLatencyMS float64         `json:"status_rpc_latency_ms"`
	Memory             json.RawMessage `json:"memory"`
}

type memoryBaselineRequest struct {
	RequestID     uint64          `json:"request_id"`
	Text          string          `json:"text"`
	RPCLatencyMS  *float64        `json:"rpc_latency_ms"`
	RPCTTFTMS     *float64        `json:"rpc_ttft_ms"`
	NativeMetrics json.RawMessage `json:"native_metrics"`
}

func TestNativeMemoryBaseline(t *testing.T) {
	binary, model, output := os.Getenv("INFERNO_NATIVE_RPC_SERVER"), os.Getenv("INFERNO_NATIVE_RPC_MODEL"), os.Getenv("INFERNO_NATIVE_MEMORY_OUTPUT")
	if binary == "" || model == "" || output == "" {
		t.Skip("opt in with scripts/test-native-memory.sh LOCAL_GGUF")
	}
	f := startRPCProcess(t, binary, func(ready string) []string {
		return []string{"--model", model, "--listen", "127.0.0.1:0", "--model-id", "local-nemotron-4b", "--ready-file", ready}
	}, 30*time.Second)

	record := struct {
		SchemaVersion int                         `json:"schema_version"`
		MeasuredAtUTC string                      `json:"measured_at_utc"`
		ModelAlias    string                      `json:"model_alias"`
		ModelBytes    int64                       `json:"model_file_bytes"`
		Observations  []memoryBaselineObservation `json:"observations"`
		Requests      []memoryBaselineRequest     `json:"requests"`
	}{SchemaVersion: 1, MeasuredAtUTC: time.Now().UTC().Format(time.RFC3339Nano), ModelAlias: "local-nemotron-4b"}
	info, err := os.Stat(model)
	if err != nil {
		t.Fatal(err)
	}
	record.ModelBytes = info.Size()

	observe := func(stage string) {
		t.Helper()
		status, err := f.client.Status(contextFor(t, time.Second))
		if err != nil || status.Response.GetMemory() == nil {
			t.Fatalf("memory stage=%s err=%v", stage, err)
		}
		data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(status.Response.Memory)
		if err != nil {
			t.Fatal(err)
		}
		record.Observations = append(record.Observations, memoryBaselineObservation{stage, status.LatencyMS, data})
	}
	generate := func(id uint64) {
		t.Helper()
		var text []byte
		result, err := f.client.Generate(contextFor(t, 5*time.Second), &pb.GenerateRequest{RequestId: id, Prompt: "What is 2 + 2? Answer with one digit.", MaxOutputTokens: 1}, func(piece []byte) error { text = append(text, piece...); return nil })
		if err != nil || !result.TransportComplete || result.Finished.GetState() != pb.TerminalState_TERMINAL_STATE_COMPLETED || result.ReceivedTokens != 1 || strings.TrimSpace(string(text)) != "4" {
			t.Fatalf("request %d text=%q result=%+v err=%v", id, text, result, err)
		}
		metrics, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(result.Finished.Metrics)
		if err != nil {
			t.Fatal(err)
		}
		record.Requests = append(record.Requests, memoryBaselineRequest{id, strings.TrimSpace(string(text)), result.LatencyMS, result.TTFTMS, metrics})
	}

	observe("backend_ready")
	generate(1)
	observe("after_request_1_cleanup")
	generate(2)
	observe("after_request_2_cleanup")
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("memory baseline written to %s", output)
}
