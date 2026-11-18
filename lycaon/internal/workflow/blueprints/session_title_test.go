package blueprints_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBlueprintLaunchNamesDraftFromLastUserIntent(t *testing.T) {
	manager, _, files, _ := testManagerWithRegistry(t)
	ctx := t.Context()
	launch := func() string {
		t.Helper()
		path, err := manager.Blueprints.ResolvePlanForStart(ctx, api.StartWorkflowRunRequest{}, testdbseed.DefaultProjectID, "", "plan", "sess-1", "")
		testutil.FailErr(t, "create blueprint", err)
		return path
	}
	first := launch()
	if !blueprint.IsProvisionalPath(first) {
		t.Fatalf("empty chat created named blueprint %q", first)
	}
	const intent = "Plan the workspace layout"
	testutil.FailErr(t, "record conversation", manager.Transcript.Append(ctx, "sess-1",
		api.Message{ID: "first", Role: api.MessageRoleUser, Content: "Earlier request"},
		api.Message{ID: "intent", Role: api.MessageRoleUser, Content: intent},
		api.Message{ID: "assistant", Role: api.MessageRoleAssistant, Content: "Assistant suggestion"}))
	next := launch()
	doc, err := files.Get(ctx, testdbseed.DefaultProjectID, next)
	testutil.FailErr(t, "read named blueprint", err)
	if title, ok := blueprintfile.DeclaredTitle(doc.Content); !ok || title != blueprint.SlugTitle(intent) || next == first {
		t.Fatalf("second blueprint %q has declared title %q", next, title)
	}
	original, err := files.Get(ctx, testdbseed.DefaultProjectID, first)
	testutil.FailErr(t, "read original blueprint", err)
	if _, ok := blueprintfile.DeclaredTitle(original.Content); ok {
		t.Fatal("naming a new draft changed the provisional one")
	}
}
