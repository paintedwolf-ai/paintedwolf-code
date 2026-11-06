package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
	"time"
)

type sourceScreen struct{}

func (sourceScreen) Screen(_ context.Context, _ ScreenDestination, req modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
	req.Messages = append([]api.Message(nil), req.Messages[1:]...)
	return req, nil
}

func TestSourceCaptureUsesScreenedProviderRequest(t *testing.T) {
	reg := newTestRegistry()
	testutil.FailErr(t, "register provider", reg.Register(&namedStubProvider{id: "source", content: "a.go"}))
	reg.SetOutboundSecretScreen(sourceScreen{})
	provider, err := reg.Get("source")
	testutil.FailErr(t, "get screened provider", err)
	target := api.NavigationTarget{ProjectID: "p", RootID: "r", Path: "first/a.go", EntryKind: api.NavigationEntryKindFile}
	other := target
	other.Path = "second/a.go"
	other.WorkerID = "worker"
	req, capture := modelcall.CaptureSourceRequest(modelcall.CompletionRequest{Messages: []api.Message{
		{Content: "first/a.go", SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{target}}},
		{Content: "second/a.go", SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{other}}},
	}})
	if got := capture.ForCompletion(&modelcall.Completion{Content: "a.go"}); len(got.Locations) != 0 {
		t.Fatal("unsent request supplied locations")
	}
	completion, err := provider.Complete(t.Context(), req)
	testutil.FailErr(t, "complete screened request", err)
	got := capture.ForCompletion(completion)
	if len(got.Locations) != 1 || got.Locations[0] != other {
		t.Fatalf("capture used pre-screened history: %+v", got)
	}
	req.Messages = req.Messages[:1]
	req.CaptureSources()
	got = capture.ForCompletion(completion)
	if len(got.Locations) != 1 || got.Locations[0] != target {
		t.Fatalf("later dispatch merged previous provider context: %+v", got)
	}
}

func TestContextRoundTripDoesNotResurrectDiscardedSources(t *testing.T) {
	target := api.NavigationTarget{ProjectID: "p", RootID: "r", WorkerID: "worker", Path: "src/a.go", EntryKind: api.NavigationEntryKindFile}
	original := api.Message{ID: "source", Content: "src/a.go", SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{target}}}
	projected := compaction.ContextMessageFromAPI(original)
	projected.Content = "summary with no source locations"
	round := compaction.ContextMessagesToAPI([]compaction.ContextMessage{projected}, []api.Message{original})
	req, capture := modelcall.CaptureSourceRequest(modelcall.CompletionRequest{Messages: round})
	req.CaptureSources()
	if got := capture.ForCompletion(&modelcall.Completion{Content: "a.go"}); len(got.Locations) != 0 {
		t.Fatalf("IR restored discarded source: %+v", got)
	}
}

func TestCompactionInputDropsSourceContextWithRows(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	var messages []compaction.ContextMessage
	for i := 0; i < 60; i++ {
		target := api.NavigationTarget{ProjectID: "p", RootID: "r", Path: fmt.Sprintf("module%d/a.go", i), EntryKind: api.NavigationEntryKindFile}
		row := compaction.ContextMessage{Role: string(api.MessageRoleTool), Content: target.Path + " " + strings.Repeat("padding ", 20)}
		row.SourceContext = &api.SourceContext{Locations: []api.NavigationTarget{target}}
		messages = append(messages, row)
	}
	input, err := compaction.BuildCompactionInput(t.Context(), compaction.SessionInfo{ID: "s"}, messages, 1024, 64)
	testutil.FailErr(t, "fit compaction input", err)
	if len(input.Messages) >= len(messages) {
		t.Fatal("fixture did not drop input rows")
	}
	req, capture := modelcall.CaptureSourceRequest(modelcall.CompletionRequest{Messages: input.Messages})
	req.CaptureSources()
	got := capture.ForCompletion(&modelcall.Completion{Content: "a.go"})
	if len(got.Locations) != len(input.Messages) {
		t.Fatalf("context locations=%d retained rows=%d", len(got.Locations), len(input.Messages))
	}
	for _, target := range got.Locations {
		if target.Path == "module59/a.go" {
			t.Fatal("dropped compaction row supplied a source")
		}
	}
}

func TestToolCompatibilityRequiresActualReceiptReplay(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "correct receipt", true: "invented receipt"}[wrong], func(t *testing.T) {
			calls := 0
			stages, err := ToolCompatibility(t.Context(), func(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
				calls++
				if req.Think != modelcall.ThinkUnset || req.MaxTokens != 0 {
					t.Fatal("probe changed application request defaults")
				}
				call := api.ToolCall{ID: "call-1", Name: "read_status", Args: map[string]any{}}
				if calls == 2 {
					var payload map[string]string
					testutil.FailErr(t, "read fixture receipt", json.Unmarshal([]byte(req.Messages[len(req.Messages)-2].Content), &payload))
					call = api.ToolCall{ID: "call-2", Name: "record_status", Args: map[string]any{"receipt": payload["receipt"]}}
					if wrong {
						call.Args["receipt"] = "invented"
					}
					if req.Messages[len(req.Messages)-1].Role != api.MessageRoleSystem {
						t.Fatal("host continuation missing")
					}
				}
				ch := make(chan modelcall.StreamChunk, 1)
				ch <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{call}, Done: true}
				close(ch)
				return ch, nil
			})
			if wrong && err == nil {
				t.Fatal("invented receipt passed")
			}
			if !wrong {
				testutil.FailErr(t, "verify tool compatibility", err)
			}
			if len(stages.Exchanges) != 2 || stages.Exchanges[1].Response == nil {
				t.Fatal("diagnostic lost the response needed to investigate replay")
			}
			if calls != 2 || len(stages.Stages) != 2 {
				t.Fatalf("calls=%d stages=%+v", calls, stages)
			}
		})
	}
}

func TestToolCompatibilityDoesNotAcceptProseOrTransportFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		stages, err := ToolCompatibility(t.Context(), func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			if failure {
				return nil, errors.New("transport unavailable")
			}
			ch := make(chan modelcall.StreamChunk, 1)
			ch <- modelcall.StreamChunk{Content: "I called the tool successfully.", Done: true}
			close(ch)
			return ch, nil
		})
		if err == nil {
			t.Fatalf("no tool call passed: %+v", stages)
		}
	}
}

func TestToolDiagnosticRequiresTerminalReceiptAndHonorsCancellation(t *testing.T) {
	partial := make(chan modelcall.StreamChunk, 1)
	partial <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{{ID: "call", Name: "read_status", Args: map[string]any{}}}}
	close(partial)
	if _, err := collectToolDiagnostic(t.Context(), partial); err == nil {
		t.Fatal("partial stream passed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := collectToolDiagnostic(ctx, make(chan modelcall.StreamChunk)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestToolCompatibilityRejectsMalformedAndTruncatedCalls(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		_, err := ToolCompatibility(t.Context(), func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			chunks := make(chan modelcall.StreamChunk, 1)
			chunks <- modelcall.StreamChunk{Done: true, ToolCalls: []api.ToolCall{{ID: "call", Name: "read_status", Args: map[string]any{}, ArgsMalformed: !truncated, ArgsTruncated: truncated}}}
			close(chunks)
			return chunks, nil
		})
		var failure *ToolCompatibilityError
		if !errors.As(err, &failure) || failure.Code != "tool_call_arguments" {
			t.Fatalf("invalid wire arguments passed: %v", err)
		}
	}
}

// deadlineProbeProvider records the deadline the request was actually sent with.
type deadlineProbeProvider struct {
	namedStubProvider
	deadline    time.Time
	hadDeadline bool
}

func (p *deadlineProbeProvider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.deadline, p.hadDeadline = ctx.Deadline()
	return p.namedStubProvider.Complete(ctx, req)
}

// holdingScreen stands in for a credential card nobody has answered yet.
type holdingScreen struct {
	check func(context.Context)
	err   error
}

func (s holdingScreen) Screen(ctx context.Context, _ ScreenDestination, req modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
	if s.check != nil {
		s.check(ctx)
	}
	return req, s.err
}

// A hold longer than the whole budget still leaves the send its full window.
func TestCallBudgetStartsAfterScreening(t *testing.T) {
	const budget = 200 * time.Millisecond
	probe := &deadlineProbeProvider{namedStubProvider: namedStubProvider{id: "lite", content: "ok"}}
	reg := newTestRegistry()
	testutil.FailErr(t, "Register", reg.Register(probe))
	budgeted := NewCallBudget(budget)
	var screened bool
	reg.SetOutboundSecretScreen(holdingScreen{check: func(ctx context.Context) {
		screened = true
		if !budgeted.started.IsZero() {
			t.Fatal("budget started during screening")
		}
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			t.Fatal("screening received a budget deadline")
		}
	}})

	p, err := reg.Get("lite")
	testutil.FailErr(t, "Get", err)
	_, err = p.Complete(WithCallBudget(context.Background(), budgeted), modelcall.CompletionRequest{Model: "lite-model"})
	testutil.FailErr(t, "Complete", err)

	if !screened {
		t.Fatal("screen hold did not complete before provider call")
	}
	if budgeted.started.IsZero() {
		t.Fatal("budget never started")
	}
	if !probe.hadDeadline {
		t.Fatal("provider received no deadline: the budget never started")
	}
	if !probe.deadline.Equal(budgeted.started.Add(budget)) {
		t.Fatalf("deadline at send = %v, want %v", probe.deadline, budgeted.started.Add(budget))
	}
	if budgeted.Expired() {
		t.Fatal("budget reported expired for a call that completed inside it")
	}
}

// An unbudgeted caller is unbounded: the registry adds no deadline of its own.
func TestCallWithoutBudgetCarriesNoDeadline(t *testing.T) {
	probe := &deadlineProbeProvider{namedStubProvider: namedStubProvider{id: "lite", content: "ok"}}
	reg := newTestRegistry()
	testutil.FailErr(t, "Register", reg.Register(probe))

	p, err := reg.Get("lite")
	testutil.FailErr(t, "Get", err)
	_, err = p.Complete(context.Background(), modelcall.CompletionRequest{Model: "lite-model"})
	testutil.FailErr(t, "Complete", err)

	if probe.hadDeadline {
		t.Fatal("provider received a deadline with no budget attached")
	}
}

// A spent budget cancels the call and says so, separating running out of time
// from the caller giving up.
func TestCallBudgetExpiryIsReported(t *testing.T) {
	slow := &budgetHoldProvider{id: "lite"}
	reg := newTestRegistry()
	testutil.FailErr(t, "Register", reg.Register(slow))

	p, err := reg.Get("lite")
	testutil.FailErr(t, "Get", err)
	budgeted := NewCallBudget(50 * time.Millisecond)
	_, err = p.Complete(WithCallBudget(context.Background(), budgeted), modelcall.CompletionRequest{Model: "lite-model"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
	if !budgeted.Expired() {
		t.Fatal("budget did not report the expiry it caused")
	}
	if !errors.Is(context.Cause(slow.callCtx), ErrCallBudgetExceeded) {
		t.Fatalf("cancel cause = %v, want the budget", context.Cause(slow.callCtx))
	}
}

// budgetHoldProvider waits for its call context to end.
type budgetHoldProvider struct {
	id      string
	callCtx context.Context //nolint:containedctx // the test asserts on the cancel cause
}

func (p *budgetHoldProvider) ID() string { return p.id }
func (p *budgetHoldProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: p.id + "-model"}}
}
func (p *budgetHoldProvider) Profile() providerprofile.Profile { return providerprofile.Profile{} }

func (p *budgetHoldProvider) Complete(ctx context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.callCtx = ctx
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *budgetHoldProvider) Stream(ctx context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	p.callCtx = ctx
	<-ctx.Done()
	return nil, ctx.Err()
}
