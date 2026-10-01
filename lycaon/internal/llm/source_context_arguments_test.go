package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSourceCaptureRetainsToolArgumentLocations(t *testing.T) {
	target := api.NavigationTarget{
		ProjectID: "p", RootID: "r", Path: "docs/My File.md", EntryKind: api.NavigationEntryKindFile,
	}
	req, capture := modelcall.CaptureSourceRequest(modelcall.CompletionRequest{Messages: []api.Message{{
		Content:       "Read `docs/My File.md`.",
		SourceContext: &api.SourceContext{Locations: []api.NavigationTarget{target}},
	}}})
	req.CaptureSources()
	completion := &modelcall.Completion{ToolCalls: []api.ToolCall{{
		ID: "read-file", Name: "read", Args: map[string]any{"path": target.Path},
	}}}
	got := capture.ForCompletion(completion)
	if len(got.Locations) != 1 || got.Locations[0] != target {
		t.Fatalf("tool argument source context = %+v", got)
	}

	completion.ToolCalls[0].Args = map[string]any{"path": "unobserved.go"}
	if got := capture.ForCompletion(completion); len(got.Locations) != 0 {
		t.Fatalf("unobserved tool argument supplied a source: %+v", got)
	}
}
