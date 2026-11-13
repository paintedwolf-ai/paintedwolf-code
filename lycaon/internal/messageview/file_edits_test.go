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
	msg := api.Message{ID: "message", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "write",
		FileEdits:        []api.FileEditSnapshot{{Path: "a.go", Before: &before, After: before + "new\n"}, {Path: "b.go", After: "created\n"}},
		FileEditsOmitted: 3,
		OverlayPromotion: &api.OverlayPromotion{Files: []api.FileEditSnapshot{{Path: "c.go", After: "one\n"}, {Path: "d.go", After: "two\n"}}},
	}}
	view := TranscriptMessage(msg)
	projected := view.ToolResult
	if projected.FileEdits != nil || projected.OverlayPromotion != nil || len(projected.FileEditPreviews) != 2 || len(projected.PromotionPreviews) != 2 || projected.FileEditsOmitted != 3 {
		t.Fatalf("file edit projection = %+v", projected)
	}
	preview := projected.FileEditPreviews[0]
	if preview.Added != 1 || preview.Removed != 0 || preview.Reference.MessageID != msg.ID || preview.Reference.ToolCallID != "write" {
		t.Fatalf("preview = %+v", preview)
	}
	// Each list is addressed from zero by its own source.
	for source, previews := range map[api.FileEditSource][]api.FileEditPreview{
		api.FileEditSourceFileEdit: projected.FileEditPreviews, api.FileEditSourceOverlayPromotion: projected.PromotionPreviews,
	} {
		for i, item := range previews {
			if item.Reference.Source != source || item.Reference.Index != i {
				t.Fatalf("%s preview %d is addressed as %+v", source, i, item.Reference)
			}
		}
	}
	raw, err := json.Marshal(view)
	testutil.FailErr(t, "encode transcript", err)
	if strings.Contains(string(raw), "private source line") || len(raw) > 2000 {
		t.Fatalf("source bodies reached transcript (%d bytes)", len(raw))
	}
	if len(msg.ToolResult.FileEdits) != 2 || msg.ToolResult.FileEdits[0].After != before+"new\n" {
		t.Fatal("stored history changed")
	}
	again := TranscriptMessage(view)
	if len(again.ToolResult.FileEditPreviews) != 2 || again.ToolResult.FileEditPreviews[0].Reference != preview.Reference {
		t.Fatal("projecting a live replay changed its reference")
	}
}
