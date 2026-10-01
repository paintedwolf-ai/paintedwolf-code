package store

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

// evidenceSummarizeCommit mints summarize#1..#N per anchor, then assigns the parent
// tool record the next ordinal so anchor handles match the wire fixture.
func evidenceSummarizeCommit(projectDir, content string, rec evidence.Record, startOrdinal int) (parentHandle string, patched string, children []evidence.Record) {
	ordinalCursor := map[string]int{rec.Kind: startOrdinal - 1}
	nextOrdinal := func(kind string) int {
		ordinalCursor[kind]++
		return ordinalCursor[kind]
	}
	patched, children = mintToolHighlightRecords(projectDir, "summarize", content, nextOrdinal)
	parentOrd := startOrdinal
	if len(children) > 0 {
		parentOrd = startOrdinal + len(children)
	}
	return evidence.FormatHandle(rec.Kind, parentOrd), patched, children
}

// evidenceCaptureFilmstripCommit mints page#1..#N per frame (with frame_index), then
// assigns the parent aggregate capture the next ordinal (summarize-style).
func evidenceCaptureFilmstripCommit(content string, rec evidence.Record, startOrdinal int) (parentHandle string, patched string, children []evidence.Record) {
	ordinalCursor := map[string]int{rec.Kind: startOrdinal - 1}
	nextOrdinal := func(kind string) int {
		ordinalCursor[kind]++
		return ordinalCursor[kind]
	}
	patched, children = mintCaptureFrameHighlightRecords(content, nextOrdinal)
	parentOrd := startOrdinal
	if len(children) > 0 {
		parentOrd = startOrdinal + len(children)
	}
	return evidence.FormatHandle(rec.Kind, parentOrd), patched, children
}

func mintCaptureFrameHighlightRecords(content string, nextOrdinal func(kind string) int) (patched string, records []evidence.Record) {
	highlights := evidence.CaptureFrameHighlightRecords(content)
	if len(highlights) == 0 {
		return content, nil
	}
	handles := make([]string, 0, len(highlights))
	for _, rec := range highlights {
		ord := nextOrdinal(rec.Kind)
		handle := evidence.FormatHandle(rec.Kind, ord)
		rec.Handle = handle
		records = append(records, rec)
		handles = append(handles, handle)
	}
	patchedOut, err := evidence.PatchCaptureFrameHandles(content, handles)
	if err != nil || strings.TrimSpace(patchedOut) == "" {
		return content, records
	}
	return patchedOut, records
}

func mintToolHighlightRecords(projectDir, toolName, content string, nextOrdinal func(kind string) int) (patched string, records []evidence.Record) {
	var highlights []evidence.Record
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "read":
		highlights = evidence.ReadHighlightRecords(projectDir, content)
	case "list_dir":
		highlights = evidence.ListHighlightRecords(projectDir, content)
	case "grep":
		highlights = evidence.GrepHighlightRecords(projectDir, content)
	case "find":
		highlights = evidence.FindHighlightRecords(projectDir, content)
	case "summarize":
		highlights = evidence.SummarizeAnchorHighlightRecords(projectDir, content)
	default:
		return content, nil
	}
	if len(highlights) == 0 {
		return content, nil
	}
	handles := make([]string, 0, len(highlights))
	for _, rec := range highlights {
		ord := nextOrdinal(rec.Kind)
		handle := evidence.FormatHandle(rec.Kind, ord)
		rec.Handle = handle
		records = append(records, rec)
		handles = append(handles, handle)
	}
	var patchedOut string
	var err error
	if strings.EqualFold(toolName, "summarize") {
		patchedOut, err = evidence.PatchSummarizeAnchorHandles(content, handles)
	} else {
		patchedOut, err = evidence.PatchReadHighlightHandles(content, handles)
	}
	if err != nil || strings.TrimSpace(patchedOut) == "" {
		return content, records
	}
	return patchedOut, records
}
