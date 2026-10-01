package native

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// reportTextIntent publishes the lines a text write will replace, in the text
// the agent read and anchored in that document revision, so the ranges follow
// anything the person types while the write waits.
func reportTextIntent(tctx tools.ToolContext, resolved projectpaths.Resolved, st sourceview.Text, exists bool, after string) {
	// A worker writes its private branch; that change reaches presence as a draft.
	if tctx.Presence == nil || strings.TrimSpace(tctx.WorkerBranchRoot) != "" {
		return
	}
	target, ok := sourceview.ReportTarget(tctx, resolved, api.AgentActivityKindEditing)
	if !ok {
		return
	}
	intent := agentpresence.Intent{Target: target, Operation: api.AgentIntentOperationEdit, Extent: api.AgentPresenceExtentWholeFile}
	if !exists {
		intent.Operation = api.AgentIntentOperationCreate
		tctx.Presence.Intents([]agentpresence.Intent{intent})
		return
	}
	if st.Editor != nil {
		intent.Document = agentpresence.Document{ID: st.Editor.ID, Revision: st.Editor.Revision}
	}
	if changes, err := documentcore.ChangedLines(st.Content, after); err == nil && len(changes) > 0 {
		intent.Extent, intent.Spans = changeExtent(changes), changeSpans(changes)
	}
	tctx.Presence.Intents([]agentpresence.Intent{intent})
}

// reportLanded names the document revision a write produced.
func reportLanded(tctx tools.ToolContext, document tools.EditorDocumentText) {
	if tctx.Presence != nil && document.ID != "" {
		tctx.Presence.Landed([]agentpresence.Document{{ID: document.ID, Revision: document.Revision}})
	}
}

func changeSpans(changes []documentcore.LineChange) []agentpresence.Span {
	spans := make([]agentpresence.Span, 0, len(changes))
	for _, change := range changes {
		if change.Insertion {
			zero := 0
			spans = append(spans, agentpresence.Span{StartLine: change.StartLine, EndLine: change.StartLine, StartCharacter: &zero, EndCharacter: &zero})
			continue
		}
		spans = append(spans, agentpresence.Span{StartLine: change.StartLine, EndLine: change.EndLine})
	}
	return spans
}

func changeExtent(changes []documentcore.LineChange) api.AgentPresenceExtent {
	for _, change := range changes {
		if !change.Insertion {
			return api.AgentPresenceExtentRange
		}
	}
	return api.AgentPresenceExtentInsertion
}

// reportMutationIntent publishes a whole-file mutation that is not a text
// rewrite: a streamed write, a delete, or a rename.
func reportMutationIntent(tctx tools.ToolContext, m agentMutation, target, from fseffect.Location, operation api.AgentIntentOperation) {
	if tctx.Presence == nil {
		return
	}
	preview := agentMutationPreview(tctx, m, target, from)
	intent := agentpresence.Intent{Target: agentpresence.Target{RootID: preview.RootID, Path: filepath.ToSlash(preview.Path)}, Operation: operation, Extent: api.AgentPresenceExtentWholeFile}
	if operation == api.AgentIntentOperationMove {
		intent.Path, intent.ToPath = filepath.ToSlash(preview.FromPath), filepath.ToSlash(preview.Path)
	}
	if intent.RootID == "" || filepath.IsAbs(intent.Path) {
		return
	}
	tctx.Presence.Target(intent.Target, api.AgentActivityKindEditing)
	tctx.Presence.Intents([]agentpresence.Intent{intent})
}
