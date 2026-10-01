package llm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type sourceScreen struct{}

func (sourceScreen) Screen(_ context.Context, _ ScreenDestination, req modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
	req.Messages = append([]api.Message(nil), req.Messages[1:]...)
	return req, nil
}

func TestSourceCaptureUsesScreenedProviderRequest(t *testing.T) {
	reg := newEmptyRegistry()
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
		messages = append(messages, compaction.ContextMessage{Role: string(api.MessageRoleTool), Content: target.Path + " " + strings.Repeat("padding ", 500), SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{target}}})
	}
	input, err := compaction.BuildCompactionInput(t.Context(), compaction.SessionInfo{ID: "s"}, messages, 512, 64)
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
		if target.Path == "module0/a.go" {
			t.Fatal("dropped compaction row supplied a source")
		}
	}
}
