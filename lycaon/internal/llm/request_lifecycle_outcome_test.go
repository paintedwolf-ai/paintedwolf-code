package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

type outcomeScriptedProvider struct {
	chunks []modelcall.StreamChunk
}

func (p *outcomeScriptedProvider) ID() string { return "p" }
func (p *outcomeScriptedProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "m"}}
}
func (p *outcomeScriptedProvider) Profile() providerprofile.Profile {
	profile := providerprofile.OpenAI()
	profile.Discovery = providerprofile.DiscoveryNone
	return profile
}
func (p *outcomeScriptedProvider) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	completion, _, err := modelcall.CollectStream(p.stream())
	return completion, err
}
func (p *outcomeScriptedProvider) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return p.stream(), nil
}
func (p *outcomeScriptedProvider) stream() <-chan modelcall.StreamChunk {
	ch := make(chan modelcall.StreamChunk, len(p.chunks))
	for _, c := range p.chunks {
		ch <- c
	}
	close(ch)
	return ch
}

// finishedPhases returns the outcome and failed fields of each finished
// phase record in the captured logs.
func finishedPhases(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		testutil.FailErr(t, "decode log line", json.Unmarshal([]byte(line), &record))
		if record["msg"] == "model request phase" && record["phase"] == "finished" {
			out = append(out, record)
		}
	}
	return out
}

// A consumer that cancels its context after the terminal chunk has still
// received a completed stream; only an error or an early stop is a failure.
func TestRequestLifecycleReportsStreamOutcome(t *testing.T) {
	cases := []struct {
		name    string
		chunks  []modelcall.StreamChunk
		cancel  bool
		outcome string
		failed  bool
	}{
		{name: "completed then canceled", chunks: []modelcall.StreamChunk{{Content: "ok", Done: true}}, cancel: true, outcome: "completed", failed: false},
		{name: "error chunk", chunks: []modelcall.StreamChunk{{Err: context.DeadlineExceeded, Done: true}}, outcome: "failed", failed: true},
		{name: "closed without terminal chunk", chunks: []modelcall.StreamChunk{{Content: "partial"}}, outcome: "abandoned", failed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs lifecycleLogBuffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })

			scripted := &outcomeScriptedProvider{chunks: tc.chunks}
			snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{Providers: map[string]modelcall.Provider{"p": scripted}})
			provider := (&Registry{}).decorateProvider(snapshot, scripted)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream, err := provider.Stream(ctx, modelcall.CompletionRequest{Model: "m"})
			testutil.FailErr(t, "stream", err)
			for range stream {
			}
			if tc.cancel {
				cancel()
			}
			phases := finishedPhases(t, logs.text())
			if len(phases) != 1 {
				t.Fatalf("finished phases = %v, want one", phases)
			}
			if phases[0]["outcome"] != tc.outcome || phases[0]["failed"] != tc.failed {
				t.Fatalf("finished = %v, want outcome %q failed %v", phases[0], tc.outcome, tc.failed)
			}
		})
	}
}
