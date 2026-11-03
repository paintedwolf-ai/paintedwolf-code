package literal

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
)

func TestSummaryBuiltinProjectsFollowupsAndContinuesCoverage(t *testing.T) {
	h := domainHarness(t)
	owner := h.owners.Domain.Summarize
	owner.Caps.Gather.MetadataNodes = 1
	owner.Caps.Pack.SubtreeFanoutMax = 1
	var observed summarize.Result
	owner.Observe = func(result summarize.Result) { observed = result }
	first := h.exec("", "summarize", "--path", ".", "--focus", "project structure")
	testutil.FailErr(t, "summarize first page", first.err)
	var response survey.SummarizeResponse
	testutil.FailErr(t, "decode first page", json.Unmarshal([]byte(first.stdout), &response))
	if len(response.NextActions) == 0 || response.Coverage.NextCursor == "" {
		t.Fatalf("missing follow-up or cursor: %s", first.stdout)
	}
	for _, action := range response.NextActions {
		if action.Tool != "command" || action.Command == "" {
			t.Fatalf("uncallable follow-up: %+v", action)
		}
	}
	for _, action := range observed.NextActions {
		if action.Tool == "command" {
			t.Fatal("command presentation changed internal routing")
		}
	}
	cursor := response.Coverage.NextCursor
	next := h.exec("", "summarize", "--path", ".", "--focus", "project structure", "--cursor", cursor)
	testutil.FailErr(t, "continue summary", next.err)
	testutil.FailErr(t, "decode continuation", json.Unmarshal([]byte(next.stdout), &response))
	if response.Coverage.Cursor != cursor {
		t.Fatalf("continuation ignored: %+v", response.Coverage)
	}
}
