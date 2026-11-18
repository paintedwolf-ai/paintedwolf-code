package command

import (
	"testing"

	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStateObservationRecordsOnlyCoverageGaps(t *testing.T) {
	var tctx tools.ToolContext
	tctx.Effects.Out = &tools.ToolInvocationOut{}
	stateObservation(tctx, nil)
	complete := api.CommandFolderCoverage{Folder: "/work", Coverage: api.SourceWindowCoverageComplete}
	stateObservation(tctx, &hostcmd.SourceWindow{Coverage: []api.CommandFolderCoverage{complete}})
	if len(tctx.Effects.Out.CardFacts) != 0 {
		t.Fatalf("a complete comparison stated a gap: %+v", tctx.Effects.Out.CardFacts)
	}
	gap := api.CommandFolderCoverage{Folder: "/srv/shared", Coverage: api.SourceWindowCoverageNotObserved, Reason: api.SourceWindowGapIoError}
	stateObservation(tctx, &hostcmd.SourceWindow{Coverage: []api.CommandFolderCoverage{complete, gap}})
	facts := tctx.Effects.Out.CardFacts
	if len(facts) != 1 || facts[0].Kind != api.ActionFactSourceObservation || len(facts[0].SourceCoverage) != 1 ||
		facts[0].SourceCoverage[0].Folder != gap.Folder {
		t.Fatalf("card facts = %+v, want only the gap", facts)
	}
}
