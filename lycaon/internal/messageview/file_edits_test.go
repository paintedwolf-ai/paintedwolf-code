package messageview

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTranscriptFileEditsCarryReferencesWithoutMutatingHistory(t *testing.T) {
	before := strings.Repeat("private source line\n", 1000)
	msg := api.Message{ID: "message", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "write", FileEdit: &api.FileEditSnapshot{Path: "a.go", Before: &before, After: before + "new\n"}}}
	view := TranscriptMessage(msg)
	projected := view.ToolResult
	if projected.FileEdit != nil || projected.FileEditPreview == nil {
		t.Fatalf("file edit projection = %+v", projected)
	}
	preview := projected.FileEditPreview
	if preview.Added != 1 || preview.Removed != 0 || preview.Reference.MessageID != msg.ID || preview.Reference.ToolCallID != "write" {
		t.Fatalf("preview = %+v", preview)
	}
	raw, err := json.Marshal(view)
	testutil.FailErr(t, "encode transcript", err)
	if strings.Contains(string(raw), "private source line") || len(raw) > 2000 {
		t.Fatalf("source bodies reached transcript (%d bytes)", len(raw))
	}
	if msg.ToolResult.FileEdit == nil || msg.ToolResult.FileEdit.After != before+"new\n" {
		t.Fatal("stored history changed")
	}
	again := TranscriptMessage(view)
	if again.ToolResult.FileEditPreview.Reference != preview.Reference {
		t.Fatal("projecting a live replay changed its reference")
	}
}
