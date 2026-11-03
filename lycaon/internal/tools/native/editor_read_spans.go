package native

import (
	"strings"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
)

// checkAndAdvanceReadSpans enforces that a surgical edit falls within the
// line spans delivered to the agent when the document read basis is partial,
// and updates those spans for any line-count delta produced by a single edit.
func checkAndAdvanceReadSpans(st *sourceview.Text, old, oldString, newString string, replaceAll bool, displayPath string) error {
	if st.Editor == nil || st.Editor.ReadWhole {
		return nil
	}
	searchFrom, line, shifted := 0, 1, 0
	basis := *st.Editor
	basis.ReadSpans = append([]documentcore.LineSpan(nil), st.Editor.ReadSpans...)
	delta := strings.Count(newString, "\n") - strings.Count(oldString, "\n")
	for {
		idx := strings.Index(old[searchFrom:], oldString)
		if idx < 0 {
			break
		}
		absIdx := searchFrom + idx
		startLine := line + strings.Count(old[searchFrom:absIdx], "\n")
		trimmed := strings.TrimSuffix(oldString, "\n")
		endLine := startLine + strings.Count(trimmed, "\n")
		if strings.HasSuffix(oldString, "\n") && !strings.HasSuffix(newString, "\n") {
			endLine++
		}
		if !basis.CoversLines(startLine, endLine) {
			return &toolrejection.ToolReject{Code: "EDITOR_DOCUMENT_READ_REQUIRED", Data: map[string]any{"path": displayPath, "document_id": st.Editor.ID}}
		}
		advanceReadSpans(st.Editor, startLine+shifted, endLine+shifted, delta)
		shifted += delta
		if !replaceAll {
			break
		}
		searchFrom = absIdx + max(len(oldString), 1)
		line = startLine + strings.Count(oldString, "\n")
	}
	return nil
}

// advanceReadSpans shifts saved coverage after an authored change. Every
// affected source line was checked before this function is called.
func advanceReadSpans(doc *tools.EditorDocumentText, start, end, delta int) {
	if doc == nil || len(doc.ReadSpans) == 0 || delta == 0 {
		return
	}
	spans := make([]documentcore.LineSpan, 0, len(doc.ReadSpans))
	for _, span := range doc.ReadSpans {
		switch {
		case span.EndLine < start:
			spans = append(spans, span)
		case span.StartLine <= start && end <= span.EndLine:
			span.EndLine += delta
			if span.EndLine >= span.StartLine {
				spans = append(spans, span)
			}
		case span.StartLine > end:
			span.StartLine += delta
			span.EndLine += delta
			spans = append(spans, span)
		}
	}
	doc.ReadSpans = spans
}
