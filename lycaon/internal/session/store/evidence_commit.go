package store

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

// evidenceSummarizeCommit mints summarize#1..#N per anchor, then assigns the parent
// tool record the next ordinal so anchor handles match the wire fixture.
func evidenceSummarizeCommit(roots evidence.CitationRoots, content string, rec evidence.Record, startOrdinal int) (parentHandle string, patched string, children []evidence.Record) {
	ordinalCursor := map[string]int{rec.Kind: startOrdinal - 1}
	nextOrdinal := func(kind string) int {
		ordinalCursor[kind]++
		return ordinalCursor[kind]
	}
	patched, children = mintToolHighlightRecords(roots, "summarize", content, nextOrdinal)
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

func mintToolHighlightRecords(roots evidence.CitationRoots, toolName, content string, nextOrdinal func(kind string) int) (patched string, records []evidence.Record) {
	var highlights []evidence.Record
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "read":
		highlights = evidence.ReadHighlightRecords(roots, content)
	case "list_dir":
		highlights = evidence.ListHighlightRecords(roots, content)
	case "grep":
		highlights = evidence.GrepHighlightRecords(roots, content)
	case "find":
		highlights = evidence.FindHighlightRecords(roots, content)
	case "summarize":
		highlights = evidence.SummarizeAnchorHighlightRecords(roots, content)
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
