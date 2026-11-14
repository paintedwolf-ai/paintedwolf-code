package api

import "slices"

// FileEditSource names the tool result list a file edit reference indexes.
type FileEditSource string

const (
	// FileEditSourceFileEdit indexes ToolResult.FileEdits.
	FileEditSourceFileEdit FileEditSource = "file_edit"
	// FileEditSourceOverlayPromotion indexes ToolResult.OverlayPromotion.Files.
	FileEditSourceOverlayPromotion FileEditSource = "overlay_promotion"
)

const (
	// MaxFileEdits bounds the files one result shows a diff for.
	MaxFileEdits = 64
	// MaxFileEditBytes bounds the before and after text one result retains.
	MaxFileEditBytes = 4 << 20
)

// RecordFileEdit adds one landed text change to a result's edits and reports
// whether the result holds it. A result holds one edit per file, from the text
// before the call's first write to the text after its last: a write that
// continues an earlier one, or that an earlier one continues, extends that
// edit. A file left as the call found it has no edit. An edit past the result's
// bounds is not held.
func RecordFileEdit(edits []FileEditSnapshot, edit FileEditSnapshot) ([]FileEditSnapshot, bool) {
	for i := range edits {
		held := &edits[i]
		if held.RootID != edit.RootID || held.Path != edit.Path {
			continue
		}
		switch {
		case continuesFileEdit(*held, edit):
			held.After, held.Deleted = edit.After, edit.Deleted
		case continuesFileEdit(edit, *held):
			held.Before = edit.Before
		default:
			continue
		}
		if fileEditUnchanged(*held) {
			return slices.Delete(edits, i, i+1), true
		}
		return edits, true
	}
	if fileEditUnchanged(edit) {
		return edits, true
	}
	size := fileEditBytes(edit)
	for _, held := range edits {
		size += fileEditBytes(held)
	}
	if len(edits) >= MaxFileEdits || size > MaxFileEditBytes {
		return edits, false
	}
	return append(edits, edit), true
}

// continuesFileEdit reports whether next was written against the text first left.
func continuesFileEdit(first, next FileEditSnapshot) bool {
	if first.Deleted {
		return next.Before == nil
	}
	return next.Before != nil && *next.Before == first.After
}

func fileEditUnchanged(edit FileEditSnapshot) bool {
	if edit.Before == nil {
		return edit.Deleted
	}
	return !edit.Deleted && *edit.Before == edit.After
}

func fileEditBytes(edit FileEditSnapshot) int {
	size := len(edit.After)
	if edit.Before != nil {
		size += len(*edit.Before)
	}
	return size
}
