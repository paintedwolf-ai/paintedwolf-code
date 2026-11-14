package command

import (
	"path/filepath"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// recordDeliveredRead states a committed delivery as text the model received:
// its lines show as read in the editor and its file joins the result's source
// context. Lines a later stage or the output window dropped were never
// delivered, so they are never stated.
func recordDeliveredRead(run *programRun, pin toolcontract.SourcePin, obs evidence.DeliveredSourceObservation) {
	location, ok := run.tc.ResolveSourceCapture(pin.SourceID)
	fact := obs.Record().SourceDelivery
	if !ok || location.RootID == "" || fact.StartLine < 1 || fact.EndLine < fact.StartLine {
		return
	}
	target := agentpresence.Target{RootID: location.RootID, Path: location.Path}
	span := agentpresence.Span{StartLine: fact.StartLine, EndLine: fact.EndLine}
	run.tc.Effects.Out.WithLock(func(out *tools.ToolInvocationOut) {
		appendDeliveredSpan(out, target, span)
		run.tc.RecordSourcePath(filepath.Join(location.Disk.Root, location.Disk.Rel), api.NavigationEntryKindFile)
	})
}

// appendDeliveredSpan keeps one read per file, however many windows of it a
// program delivered.
func appendDeliveredSpan(out *tools.ToolInvocationOut, target agentpresence.Target, span agentpresence.Span) {
	for i := range out.SourceReads {
		read := &out.SourceReads[i]
		if read.Target == target && read.Extent == api.AgentPresenceExtentRange && read.Document == (agentpresence.Document{}) && len(read.ItemSpans) == 0 {
			read.Spans = append(read.Spans, span)
			return
		}
	}
	out.SourceReads = append(out.SourceReads, agentpresence.Read{Target: target, Extent: api.AgentPresenceExtentRange, Spans: []agentpresence.Span{span}})
}
