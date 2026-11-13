package sourceapi

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// A reference names its list and a zero-based place in it; anything else finds nothing.
func TestReadChatFileEditResolvesEachListFromZero(t *testing.T) {
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), wire.CreateSessionRequest{ProviderID: "fixture", Model: "large"}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "store result", mem.AppendMessages(t.Context(), sess.ID, wire.Message{
		ID: "m1", Role: wire.MessageRoleTool,
		ToolResult: &wire.ToolResult{
			ToolCallID:       "c1",
			FileEdits:        []wire.FileEditSnapshot{{Path: "a.go", After: "a"}, {Path: "b.go", After: "b"}},
			OverlayPromotion: &wire.OverlayPromotion{Files: []wire.FileEditSnapshot{{Path: "p.go", After: "p"}, {Path: "q.go", After: "q"}}},
		},
	}, wire.Message{ID: "m2", Role: wire.MessageRoleTool, ToolResult: &wire.ToolResult{ToolCallID: "c2", FileEdits: []wire.FileEditSnapshot{{Path: "only.go", After: "x"}}}}))
	comparisons := &Comparisons{SessionStore: mem}
	ref := func(message, call string, source wire.FileEditSource, index int) wire.FileEditReference {
		return wire.FileEditReference{MessageID: message, ToolCallID: call, Source: source, Index: index}
	}
	for want, reference := range map[string]wire.FileEditReference{
		"a.go":    ref("m1", "c1", wire.FileEditSourceFileEdit, 0),
		"b.go":    ref("m1", "c1", wire.FileEditSourceFileEdit, 1),
		"p.go":    ref("m1", "c1", wire.FileEditSourceOverlayPromotion, 0),
		"q.go":    ref("m1", "c1", wire.FileEditSourceOverlayPromotion, 1),
		"only.go": ref("m2", "c2", wire.FileEditSourceFileEdit, 0),
	} {
		edit, err := comparisons.readChatFileEdit(t.Context(), sess.ID, reference)
		testutil.FailErr(t, "read "+want, err)
		if edit.Path != want {
			t.Fatalf("%+v resolved to %s, want %s", reference, edit.Path, want)
		}
	}
	for name, reference := range map[string]wire.FileEditReference{
		"past the end":           ref("m1", "c1", wire.FileEditSourceFileEdit, 2),
		"before the start":       ref("m1", "c1", wire.FileEditSourceOverlayPromotion, -1),
		"a list the row lacks":   ref("m2", "c2", wire.FileEditSourceOverlayPromotion, 0),
		"an unknown source":      ref("m1", "c1", "worker", 0),
		"another call's message": ref("m1", "c2", wire.FileEditSourceFileEdit, 0),
		"a missing message":      ref("m9", "c1", wire.FileEditSourceFileEdit, 0),
	} {
		if edit, err := comparisons.readChatFileEdit(t.Context(), sess.ID, reference); err == nil {
			t.Fatalf("%s resolved to %+v", name, edit)
		}
	}
}
