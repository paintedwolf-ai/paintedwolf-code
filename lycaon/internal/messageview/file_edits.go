package messageview

import (
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
)

// File edit bodies stay in recorded history; transcript projections carry references.
func projectFileEdits(msg api.Message) api.Message {
	out := msg
	out.ToolCalls = slices.Clone(msg.ToolCalls)
	for i := range out.ToolCalls {
		call := &out.ToolCalls[i]
		if call.Name == "write" || call.Name == "edit" {
			call.Args = sourcePresentationArgs(call.Args)
		}
	}
	if msg.ToolResult == nil {
		return out
	}
	result := *msg.ToolResult
	if result.FileEdit != nil {
		result.ToolArgs = sourcePresentationArgs(result.ToolArgs)
		preview := fileEditPreview(*result.FileEdit, api.FileEditReference{MessageID: msg.ID, ToolCallID: result.ToolCallID})
		result.FileEditPreview = &preview
		result.FileEdit = nil
	}
	if result.OverlayPromotion != nil {
		result.PromotionPreviews = make([]api.FileEditPreview, 0, len(result.OverlayPromotion.Files))
		for j, edit := range result.OverlayPromotion.Files {
			result.PromotionPreviews = append(result.PromotionPreviews, fileEditPreview(edit, api.FileEditReference{MessageID: msg.ID, ToolCallID: result.ToolCallID, Index: j + 1}))
		}
		result.OverlayPromotion = nil
	}
	out.ToolResult = &result
	return out
}

func fileEditPreview(edit api.FileEditSnapshot, ref api.FileEditReference) api.FileEditPreview {
	before := ""
	if edit.Before != nil {
		before = *edit.Before
	}
	preview := api.FileEditPreview{Path: edit.Path, RootID: edit.RootID, Reference: ref, BeforeSHA256: sourcecomparison.Hash(before), AfterSHA256: sourcecomparison.Hash(edit.After), Created: edit.Before == nil, Deleted: edit.Deleted}
	added, removed, err := sourcecomparison.Counts(before, edit.After)
	if err == nil {
		preview.Added, preview.Removed = added, removed
	}
	return preview
}

// Source bodies are read through the comparison; raw argument inspection uses the retained tool record.
func sourcePresentationArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for key, value := range args {
		switch key {
		case "content", "old_string", "new_string", "patch":
			continue
		}
		out[key] = value
	}
	return out
}
