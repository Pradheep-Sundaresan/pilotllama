package backendprocess

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	pb "inferno.local/controlplane/gen/inferno/v1"
)

// Opt-in: one production managed child using the explicit continuous scheduler.
func TestNativeContinuousBatching(t *testing.T) {
	binary, model, draft := os.Getenv("INFERNO_NATIVE_RPC_SERVER"), os.Getenv("INFERNO_NATIVE_RPC_MODEL"), os.Getenv("INFERNO_NATIVE_RPC_DRAFT_MODEL")
	if binary == "" || model == "" {
		t.Skip("opt in with scripts/test-native-batching.sh EXISTING_LOCAL_GGUF")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	manager, err := NewManager(ctx, Config{Executable: binary, Model: model, DraftModel: draft, ModelID: "local-nemotron-4b",
		StateDir: t.TempDir(), Batching: "continuous", QualifyContinuous: true,
		StartupTimeout: 30 * time.Second, ShutdownTimeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := manager.rpc()
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		text, detail      string
		tokens            uint32
		state             pb.TerminalState
		drafted, accepted uint64
		hasSpeculative    bool
		err               error
	}
	run := func(id uint64, prompt string, output uint32, first chan<- struct{}) outcome {
		var text strings.Builder
		obs, err := client.Generate(ctx, &pb.GenerateRequest{RequestId: id, Prompt: prompt, MaxOutputTokens: output}, func(piece []byte) error {
			text.Write(piece)
			if first != nil {
				select {
				case first <- struct{}{}:
				default:
				}
			}
			return nil
		})
		metrics := obs.Finished.GetMetrics()
		return outcome{text: text.String(), tokens: metrics.GetOutputTokens(), state: obs.Finished.GetState(),
			detail: obs.Finished.GetError().GetMessage(), drafted: metrics.GetSpeculativeDraftedTokens(),
			accepted: metrics.GetSpeculativeAcceptedTokens(), hasSpeculative: metrics.SpeculativeDraftedTokens != nil,
			err: err}
	}

	start := make(chan struct{})
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	for _, item := range []struct {
		id     uint64
		prompt string
	}{{1, "Explain in detail why local AI is useful for application one."}, {2, "Explain in detail why local AI is useful for application two."}} {
		workers.Add(1)
		go func(id uint64, prompt string) { defer workers.Done(); <-start; results <- run(id, prompt, 128, nil) }(item.id, item.prompt)
	}
	close(start)
	workers.Wait()
	close(results)
	for result := range results {
		if result.err != nil || result.state != pb.TerminalState_TERMINAL_STATE_COMPLETED || result.tokens == 0 {
			t.Fatalf("parallel result: %+v", result)
		}
		if draft != "" && (!result.hasSpeculative || result.drafted == 0 || result.accepted > result.drafted) {
			t.Fatalf("parallel speculative measurements: %+v", result)
		}
	}

	first := make(chan struct{}, 1)
	cancelledDone, survivorDone := make(chan outcome, 1), make(chan outcome, 1)
	go func() { cancelledDone <- run(3, "Count upward from one using words separated by spaces.", 64, first) }()
	go func() { survivorDone <- run(4, "What is 3 + 3? Reply with just the number.", 8, nil) }()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := client.Cancel(ctx, 3); err != nil {
		t.Fatal(err)
	}
	cancelled, survivor := <-cancelledDone, <-survivorDone
	if cancelled.err != nil || cancelled.state != pb.TerminalState_TERMINAL_STATE_CANCELLED {
		t.Fatalf("cancelled: %+v", cancelled)
	}
	if survivor.err != nil || survivor.state != pb.TerminalState_TERMINAL_STATE_COMPLETED {
		t.Fatalf("survivor: %+v", survivor)
	}
	follow := run(5, "What is 2 + 2? Reply with just the number.", 4, nil)
	if follow.err != nil || follow.state != pb.TerminalState_TERMINAL_STATE_COMPLETED || follow.tokens == 0 {
		t.Fatalf("follow-up: %+v", follow)
	}
}
