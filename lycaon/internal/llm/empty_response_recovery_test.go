package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTerminalEmptyRecoverySharesRetryAllowance(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tc := range []struct {
			name                                         string
			terminal, payload, native, disabled, unwired bool
			reason                                       string
			wantCalls, wantRecovery                      int
		}{
			{name: "terminal", terminal: true, wantCalls: 2, wantRecovery: 1},
			{name: "nonterminal", wantCalls: 2},
			{name: "partial", terminal: true, payload: true, wantCalls: 1},
			{name: "native", terminal: true, native: true, wantCalls: 1},
			{name: "disabled", terminal: true, disabled: true, wantCalls: 1},
			{name: "utility", terminal: true, unwired: true, wantCalls: 2},
			{name: "output_limit", terminal: true, reason: "length", wantCalls: 1},
		} {
			t.Run(tc.name+map[bool]string{true: "/stream", false: "/complete"}[streaming], func(t *testing.T) {
				backing := make([]api.Message, 2, 8)
				backing[0] = api.Message{Role: api.MessageRoleSystem, Content: "stable prefix"}
				backing[1] = api.Message{Role: api.MessageRoleUser, Content: "task"}
				all := backing[:cap(backing)]
				all[2] = api.Message{Content: "caller sentinel"}
				original := append([]api.Message(nil), all...)
				count := 0
				req := modelcall.CompletionRequest{Model: "model", Messages: backing, EmptyResponseRecovery: func(context.Context) (string, error) { count++; return "host repair", nil }}
				if tc.unwired {
					req.EmptyResponseRecovery = nil
					req.Composition = modelcall.CompositionHostUtility
				}
				var requests [][]api.Message
				inner := &responseRetryStub{observeRequest: func(r modelcall.CompletionRequest) {
					requests = append(requests, append([]api.Message(nil), r.Messages...))
				}}
				empty := func() *failure.ProviderEmptyCompletionError {
					return &failure.ProviderEmptyCompletionError{Terminal: tc.terminal, Retryable: true, Reason: tc.reason}
				}
				inner.complete = func(int) (*modelcall.Completion, error) {
					c := &modelcall.Completion{Usage: modelcall.TokenUsage{CompletionTokens: 7}}
					if tc.payload {
						c.Content = "partial"
					}
					if tc.native {
						c.ToolCalls = []api.ToolCall{{ID: "call", Name: "read"}}
					}
					return c, empty()
				}
				inner.stream = func(int) (<-chan modelcall.StreamChunk, error) {
					chunk := modelcall.StreamChunk{Reasoning: "<tool_call>write secret</tool_call>", Usage: modelcall.TokenUsage{CompletionTokens: 7}}
					if tc.payload {
						chunk.Content = "partial"
					}
					if tc.native {
						chunk.ToolCalls = []api.ToolCall{{ID: "call", Name: "read"}}
					}
					return retryChunkStream(chunk, modelcall.StreamChunk{Err: empty(), Done: true}), nil
				}
				policy := retryEmptyOncePolicy()
				if tc.disabled {
					policy.Transport.MaxRetries = 0
				}
				provider := &responseRetryProvider{inner: inner, policy: policy}
				var got *modelcall.Completion
				var err error
				if streaming {
					var stream <-chan modelcall.StreamChunk
					stream, err = provider.Stream(t.Context(), req)
					testutil.FailErr(t, "start stream", err)
					got, _, err = modelcall.CollectStream(stream)
				} else {
					got, err = provider.Complete(t.Context(), req)
				}
				if !errors.Is(err, failure.ErrProviderEmptyCompletion) {
					t.Fatalf("terminal error lost: %v", err)
				}
				if len(requests) != tc.wantCalls || count != tc.wantRecovery {
					t.Fatalf("attempts=%d recovery=%d", len(requests), count)
				}
				if got != nil && got.Usage.CompletionTokens != 7*tc.wantCalls {
					t.Fatalf("usage=%+v", got.Usage)
				}
				if !reflect.DeepEqual(all, original) {
					t.Fatal("caller backing slice changed")
				}
				for i, messages := range requests {
					if !reflect.DeepEqual(messages[:2], backing) {
						t.Fatal("stable prefix changed")
					}
					want := 2
					if i > 0 && tc.wantRecovery > 0 {
						want = 3
					}
					if len(messages) != want {
						t.Fatalf("attempt%d messages=%d want%d", i, len(messages), want)
					}
					if want == 3 {
						m := messages[2]
						if m.Content != "host repair" || m.Origin != api.MessageOriginHost || m.Authority != api.ContentAuthoritySystem {
							t.Fatalf("repair provenance=%+v", m)
						}
					}
					for _, m := range messages {
						if strings.Contains(m.Content, "<tool_call>") {
							t.Fatal("reasoning became input")
						}
					}
				}
			})
		}
	}
}

func TestEmptyRecoveryFailureDoesNotDispatchExtraAttempt(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		provider := &responseRetryProvider{inner: &responseRetryStub{}, policy: retryEmptyOncePolicy()}
		req := modelcall.CompletionRequest{EmptyResponseRecovery: func(context.Context) (string, error) {
			if oversized {
				return strings.Repeat("x", 4097), nil
			}
			return "", errors.New("policy evaluation failed")
		}}
		retry, err := provider.prepareResponseRetry(t.Context(), &req, 0, &failure.ProviderEmptyCompletionError{Terminal: true}, true)
		if err == nil || retry {
			t.Fatalf("invalid repair admitted: retry=%v err=%v", retry, err)
		}
	}
}

func TestTerminalEmptyRecoveryCapturesEachActualRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", path)
	observability.CloseLLMDebug()
	t.Cleanup(observability.CloseLLMDebug)
	var sent [][]api.Message
	inner := &responseRetryStub{observeRequest: func(req modelcall.CompletionRequest) {
		sent = append(sent, append([]api.Message(nil), req.Messages...))
	}, complete: func(call int) (*modelcall.Completion, error) {
		if call == 1 {
			return &modelcall.Completion{Reasoning: "call:write ignored"}, &failure.ProviderEmptyCompletionError{Terminal: true, Retryable: true, Reason: "stop"}
		}
		return &modelcall.Completion{Content: "src/a.go"}, nil
	}}
	provider := &responseRetryProvider{inner: wrapProviderIfDebug(inner), policy: retryEmptyOncePolicy()}
	location := api.NavigationTarget{ProjectID: "project", RootID: "root", Path: "src/a.go", EntryKind: api.NavigationEntryKindFile}
	req, capture := modelcall.CaptureSourceRequest(modelcall.CompletionRequest{Model: "model", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "src/a.go", SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{location}}}}, EmptyResponseRecovery: func(context.Context) (string, error) { return "host repair", nil }})
	got, err := provider.Complete(t.Context(), req)
	testutil.FailErr(t, "complete repaired request", err)
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read physical request capture", err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || len(sent) != 2 {
		t.Fatalf("captures=%d requests=%d", len(lines), len(sent))
	}
	for i, line := range lines {
		var logged struct {
			Messages []api.Message `json:"messages"`
		}
		testutil.FailErr(t, "decode request capture", json.Unmarshal([]byte(line), &logged))
		if len(logged.Messages) != len(sent[i]) {
			t.Fatalf("attempt%d captured messages=%d sent=%d", i, len(logged.Messages), len(sent[i]))
		}
		for n, m := range sent[i] {
			if logged.Messages[n].Content != m.Content {
				t.Fatalf("attempt%d message%d capture differs", i, n)
			}
		}
	}
	if len(sent[1]) != 2 || sent[1][1].Content != "host repair" {
		t.Fatal("repair missing from actual second request")
	}
	source := capture.ForCompletion(got)
	if len(source.Locations) != 1 || source.Locations[0] != location {
		t.Fatalf("repair lost source identity: %+v", source)
	}
}
