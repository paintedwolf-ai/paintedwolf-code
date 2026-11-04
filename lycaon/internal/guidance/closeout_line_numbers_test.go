package guidance

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// The shapes command output leaves in the ledger: `cat -n` makes one numbered record
// per line, as in the live runs, and plain `cat` one unnumbered record per file.
func catNumbered(first int, path string, lines ...int) []evidence.Record {
	out := make([]evidence.Record, 0, len(lines))
	for i, line := range lines {
		out = append(out, evidence.Record{
			Handle: fmt.Sprintf("read#%d", first+i), Kind: "read", Shape: evidence.ShapeFileRegion, SourceTool: "command", Path: path,
			Body: []string{fmt.Sprintf("line %d of the file", line)}, LineRanges: []evidence.LineRange{{Start: line, End: line}},
			SourceDelivery: &evidence.SourceDelivery{ResponseID: "r", DeliveryID: fmt.Sprintf("d%d", line), StartLine: line, EndLine: line, LineNumbered: true},
		})
	}
	return out
}

func catPlain(handle, path string, last int) evidence.Record {
	body := make([]string, 0, last)
	for line := 1; line <= last; line++ {
		body = append(body, fmt.Sprintf("line %d of the file", line))
	}
	return evidence.Record{
		Handle: handle, Kind: "read", Shape: evidence.ShapeFileRegion, SourceTool: "command", Path: path,
		Body: body, LineRanges: []evidence.LineRange{{Start: 1, End: last}},
		SourceDelivery: &evidence.SourceDelivery{ResponseID: "r", DeliveryID: handle, StartLine: 1, EndLine: last},
	}
}

func lineNumberOutcome(t *testing.T, source string, ev evidence.Ledger) OccurrenceResolution {
	t.Helper()
	_, resolved := repairDraft(t, source, CloseoutEvidence{Ledger: ev})
	if len(resolved.Occurrences) != 1 {
		t.Fatalf("occurrences of %q = %+v", source, resolved.Occurrences)
	}
	return resolved.Occurrences[0]
}

func TestRangeAcrossRecordsAndNumberedOutput(t *testing.T) {
	t.Parallel()
	const path = "cmd/gateway/main.go"
	numbered := evidence.AssembleLedger(catNumbered(1, path, 24, 25, 26, 27, 30))
	plain := evidence.AssembleLedger([]evidence.Record{catPlain("read#1", path, 36)})
	both := evidence.AssembleLedger(append([]evidence.Record{catPlain("read#1", path, 36)}, catNumbered(2, path, 24, 25, 26, 27)...))
	superseded := catNumbered(1, path, 24, 25)
	superseded[1].SupersededBy = "edit#1"
	stale := evidence.AssembleLedger(superseded)
	for _, tc := range []struct {
		name, source string
		ev           evidence.Ledger
		want         CitationOutcome
		unnumbered   bool
	}{
		{"path range spans numbered records", "[cmd/gateway/main.go:24-27]", numbered, CitationGrounded, false},
		{"one numbered line", "[cmd/gateway/main.go:27]", numbered, CitationGrounded, false},
		{"gap between records is rejected", "[cmd/gateway/main.go:26-30]", numbered, CitationUnverifiable, false},
		{"range past the observation is rejected", "[cmd/gateway/main.go:27-28]", numbered, CitationUnverifiable, false},
		{"a handle keeps to its own coverage", "[read#1:24-27]", numbered, CitationUnverifiable, false},
		{"a handle covers its own line", "[read#1:24]", numbered, CitationGrounded, false},
		{"a superseded record is no support", "[cmd/gateway/main.go:24-25]", stale, CitationUnverifiable, false},
		{"unnumbered output refuses a line", "[cmd/gateway/main.go:10]", plain, CitationUnverifiable, true},
		{"unnumbered output refuses a range", "[cmd/gateway/main.go:10-12]", plain, CitationUnverifiable, true},
		{"unnumbered output refuses a handle coordinate", "[read#1:10]", plain, CitationUnverifiable, true},
		{"unnumbered output supports the bare path", `["cmd/gateway/main.go"]`, plain, CitationGrounded, false},
		{"unnumbered output supports the bare handle", "[read#1]", plain, CitationGrounded, false},
		{"numbered output beside it decides", "[cmd/gateway/main.go:24-27]", both, CitationGrounded, false},
		{"unnumbered lines do not fill a numbered gap", "[cmd/gateway/main.go:27-28]", both, CitationUnverifiable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := lineNumberOutcome(t, tc.source, tc.ev)
			if got.Outcome != tc.want || got.Resolution.UnnumberedOutput != tc.unnumbered {
				t.Fatalf("outcome = %+v, want %s unnumbered=%t", got, tc.want, tc.unnumbered)
			}
		})
	}
}

// The repair the refusal offers has to work: the coordinate goes and the path stays.
func TestUnnumberedCoordinateRepairsToItsPath(t *testing.T) {
	t.Parallel()
	const path = "internal/client/client.go"
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{catPlain("read#7", path, 36)})}
	opts := DraftParseOptions(evidence.CitationRoots{}, ev.Ledger)
	draft := ParseCloseoutDraft("The constant [internal/client/client.go:9] caps it.", opts)
	resolved := ResolveDraftCitations(evidence.CitationRoots{}, "implement_investigate", draft, ev, nil)
	data := CloseoutRepairHintData(evidence.CitationRoots{}, draft, resolved, ev, []string{path + ":9"})
	targets := data["repair_targets"].([]map[string]any)
	if len(targets) != 1 || targets[0]["unnumbered"] != true || targets[0]["web_link"] != false {
		t.Fatalf("targets = %+v", targets)
	}
	var choices []citationRepairObservation
	testutil.FailErr(t, "decode choices", json.Unmarshal([]byte(data["repair_observations"].(string)), &choices))
	if len(choices) != 1 || choices[0].Evidence != "read#7" || choices[0].Lines != "" {
		t.Fatalf("choices offer lines the reader never saw: %+v", choices)
	}
	retained := RetainedCloseout{Active: true}
	retained.Retain(draft)
	for _, directive := range []string{"[cite: c1 => " + path + "]", "[cite: c1 => read#7]"} {
		read := ReadCloseoutReport(directive, retained, false, opts)
		if len(read.RepairDefects) != 0 {
			t.Fatalf("%s refused: %+v", directive, read.RepairDefects)
		}
		after := ResolveDraftCitations(evidence.CitationRoots{}, "implement_investigate", read.Draft, ev, nil)
		if len(after.Rejected()) != 0 || len(after.Occurrences) != 1 {
			t.Fatalf("%s left %+v", directive, after)
		}
		if got := PresentCloseoutMarkdown(read.Draft, after); got != "The constant ["+path+"]("+path+") caps it." {
			t.Fatalf("%s presented %q", directive, got)
		}
	}
}

// A worker's typed finding is held to the same rule unless an excerpt checks the line.
func TestWorkerCoordinateNeedsNumbersOrAnExcerpt(t *testing.T) {
	t.Parallel()
	const path = "a.go"
	plain := evidence.AssembleLedger([]evidence.Record{catPlain("read#1", path, 5)})
	mixed := evidence.AssembleLedger(append([]evidence.Record{catPlain("read#1", path, 5)}, catNumbered(2, path, 3)...))
	for _, tc := range []struct {
		name    string
		finding WorkerFindingInput
		ev      evidence.Ledger
		want    bool
	}{
		{"path and line on unnumbered output", WorkerFindingInput{Path: path, Line: 3}, plain, false},
		{"handle and line on unnumbered output", WorkerFindingInput{Evidence: "read#1", Line: 3}, plain, false},
		{"an excerpt checks the line", WorkerFindingInput{Path: path, Line: 3, Excerpt: "line 3 of the file"}, plain, true},
		{"a numbered record covers the line", WorkerFindingInput{Path: path, Line: 3}, mixed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := resolveWorkerFindingCitation(evidence.CitationRoots{}, tc.finding, tc.ev)
			if res.Verdict.Grounded() != tc.want {
				t.Fatalf("resolution = %+v, want grounded=%t", res, tc.want)
			}
		})
	}
}

func TestInlineCodeCitationResolvesOrStaysCode(t *testing.T) {
	t.Parallel()
	const path = "cmd/gateway/main.go"
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger(append(catNumbered(1, path, 21, 22), catPlain("read#3", "internal/limit/limit.go", 9)))}
	for _, tc := range []struct {
		name, source, want string
		citations          int
	}{
		{"live investigate shape", "The handler sets 30s at `cmd/gateway/main.go:21` today.", "The handler sets 30s at [cmd/gateway/main.go:21](cmd/gateway/main.go#L21) today.", 1},
		{"a range across records", "See `cmd/gateway/main.go:21-22`.", "See [cmd/gateway/main.go:21-22](cmd/gateway/main.go#L21-L22).", 1},
		{"unsupported line stays code", "See `cmd/gateway/main.go:99`.", "See `cmd/gateway/main.go:99`.", 0},
		{"unnumbered read stays code", "See `internal/limit/limit.go:4`.", "See `internal/limit/limit.go:4`.", 0},
		{"unobserved path stays code", "See `nowhere/at.go:4`.", "See `nowhere/at.go:4`.", 0},
		{"host and port stays code", "Listens on `example.test:8080`.", "Listens on `example.test:8080`.", 0},
		{"non-standalone stays code", "See x`cmd/gateway/main.go:21`.", "See x`cmd/gateway/main.go:21`.", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			draft, resolved := repairDraft(t, tc.source, ev)
			if draft.CitationCount() != tc.citations || len(resolved.Rejected()) != 0 || len(draft.Issues) != 0 {
				t.Fatalf("draft = %+v, resolved = %+v", draft.Occurrences, resolved)
			}
			if got := PresentCloseoutMarkdown(draft, resolved); got != tc.want {
				t.Fatalf("presented %q, want %q", got, tc.want)
			}
		})
	}
}

// A retained draft outlives its parse. A span the ledger no longer supports is code
// again, in the outcome and on display, never a rejection.
func TestRetainedInlineCodeNeverRejects(t *testing.T) {
	t.Parallel()
	const path = "a.go"
	before := evidence.AssembleLedger(catNumbered(1, path, 2))
	draft := ParseCloseoutDraft("See `a.go:2` here.", DraftParseOptions(evidence.CitationRoots{}, before))
	if draft.CitationCount() != 1 {
		t.Fatalf("occurrences = %+v", draft.Occurrences)
	}
	after := CloseoutEvidence{Ledger: evidence.AssembleLedger(catNumbered(1, "other.go", 2))}
	resolved := ResolveDraftCitations(evidence.CitationRoots{}, "implement_investigate", draft, after, nil)
	if len(resolved.Rejected()) != 0 {
		t.Fatalf("rejected = %+v", resolved.Rejected())
	}
	if got := PresentCloseoutMarkdown(draft, resolved); got != "See `a.go:2` here." {
		t.Fatalf("presented %q", got)
	}
}

func TestWorkspaceLinkRepairTakesHandleOrPath(t *testing.T) {
	t.Parallel()
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger(catNumbered(5, "a/client.go", 30))}
	opts := DraftParseOptions(evidence.CitationRoots{}, ev.Ledger)
	draft := ParseCloseoutDraft("The [deadline](client.go#L30) is ignored, per [docs](https://example.test/unseen).", opts)
	resolved := ResolveDraftCitations(evidence.CitationRoots{}, "implement_investigate", draft, ev, nil)
	targets := CloseoutRepairHintData(evidence.CitationRoots{}, draft, resolved, ev, nil)["repair_targets"].([]map[string]any)
	if len(targets) != 2 || targets[0]["web_link"] != false || targets[1]["web_link"] != true {
		t.Fatalf("targets = %+v", targets)
	}
	retained := RetainedCloseout{Active: true}
	retained.Retain(draft)
	for _, tc := range []struct {
		name, directive, want string
		defect                RepairDefectReason
	}{
		{name: "handle keeps the label", directive: "[cite: c1 => read#5:30]", want: "The deadline [read#5:30] is ignored"},
		{name: "path retargets the link", directive: "[cite: c1 => a/client.go:30]", want: "The [deadline](a/client.go#L30) is ignored"},
		{name: "a path with no line drops nothing the label says", directive: "[cite: c1 => a/client.go]", want: "The [deadline](a/client.go) is ignored"},
		{name: "a url on a workspace link is the real mismatch", directive: "[cite: c1 => <https://example.test/x>]", defect: RepairWrongKind},
		{name: "a handle on a web link is the real mismatch", directive: "[cite: c2 => read#5:30]", defect: RepairWrongKind},
	} {
		read := ReadCloseoutReport(tc.directive, retained, false, opts)
		if tc.defect != "" {
			if len(read.RepairDefects) != 1 || read.RepairDefects[0].Reason != tc.defect {
				t.Fatalf("%s: defects = %+v", tc.name, read.RepairDefects)
			}
			continue
		}
		if len(read.RepairDefects) != 0 || !strings.Contains(read.Draft.Synthesis, tc.want) {
			t.Fatalf("%s: read = %+v", tc.name, read)
		}
		if got, want := citationmd.Narrative(read.Draft.Parsed), citationmd.Narrative(draft.Parsed); got != want {
			t.Fatalf("%s changed the narrative: %q, want %q", tc.name, got, want)
		}
	}
}

// A label that is only a coordinate says nothing but the coordinate. Live, a directive
// left "main.go:19-23" as prose beside the repaired citation, on lines no one verified.
func TestCoordinateLabelDoesNotOutliveItsCoordinate(t *testing.T) {
	t.Parallel()
	const path = "cmd/gateway/main.go"
	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{catPlain("read#3", path, 31)})}
	opts := DraftParseOptions(evidence.CitationRoots{}, ev.Ledger)
	draft := ParseCloseoutDraft("The handler [cmd/gateway/main.go:19-23](cmd/gateway/main.go#L19-L23) builds it.", opts)
	retained := RetainedCloseout{Active: true}
	retained.Retain(draft)
	want := "The handler [" + path + "](" + path + ") builds it."
	for _, directive := range []string{"[cite: c1 => read#3]", "[cite: c1 => " + path + "]"} {
		read := ReadCloseoutReport(directive, retained, false, opts)
		if len(read.RepairDefects) != 0 {
			t.Fatalf("%s: %+v", directive, read.RepairDefects)
		}
		after := ResolveDraftCitations(evidence.CitationRoots{}, "implement_investigate", read.Draft, ev, nil)
		if got := PresentCloseoutMarkdown(read.Draft, after); got != want {
			t.Fatalf("%s presented %q, want %q", directive, got, want)
		}
	}
	// Unrepaired, as when no turn is left, the unverified lines still do not show.
	resolved := ResolveDraftCitations(evidence.CitationRoots{}, "implement_investigate", draft, ev, nil)
	if got := PresentCloseoutMarkdown(draft, resolved); got != "The handler "+path+" builds it." {
		t.Fatalf("rejected link presented %q", got)
	}
}

// A verified excerpt is stronger evidence than a printed number, in both paths.
func TestVerifiedExcerptSupportsACoordinateOnUnnumberedOutput(t *testing.T) {
	t.Parallel()
	const path = "a.go"
	ev := evidence.AssembleLedger([]evidence.Record{catPlain("read#1", path, 5)})
	for _, tc := range []struct {
		name, source string
		want         CitationOutcome
	}{
		{"matching text at the line", `[a.go:3 "line 3 of the file"]`, CitationGrounded},
		{"matching text in the range", `[a.go:2-4 "line 3 of the file"]`, CitationGrounded},
		{"a handle", `[read#1:3 "line 3 of the file"]`, CitationGrounded},
		{"text from another line", `[a.go:3 "line 4 of the file"]`, CitationUnverifiable},
		{"text observed nowhere", `[a.go:3 "something else entirely"]`, CitationUnverifiable},
		{"no excerpt", `[a.go:3]`, CitationUnverifiable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := lineNumberOutcome(t, tc.source, ev); got.Outcome != tc.want {
				t.Fatalf("outcome = %+v, want %s", got, tc.want)
			}
		})
	}
}

// A refused coordinate on a worker's record offers a repair that can actually be sent.
func TestRepairOffersWhatTheCoordinatorCanSend(t *testing.T) {
	t.Parallel()
	const path = "cmd/gateway/main.go"
	worker := evidence.NamespaceLedger(evidence.AssembleLedger(append(catNumbered(1, path, 13), catPlain("read#2", path, 31))), "leg-a")
	numbered := evidence.NamespaceLedger(evidence.AssembleLedger(catNumbered(1, path, 13, 14, 15)), "leg-a")
	own := evidence.AssembleLedger([]evidence.Record{catPlain("read#1", path, 31)})
	for _, tc := range []struct {
		name, source string
		ev           evidence.Ledger
		use          string
		worker       bool
	}{
		{"live shape: a line no numbered record of the worker's shows", "[leg-a:read#1:14]", worker, "leg-a:read#2", true},
		{"a range the worker's numbered records cover by path", "[leg-a:read#1:14-15]", numbered, path + ":14-15", false},
		{"the coordinator's own read", "[cmd/gateway/main.go:14]", own, "read#1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := CloseoutEvidence{Ledger: tc.ev}
			draft, resolved := repairDraft(t, "It holds "+tc.source+".", ev)
			targets, _ := CloseoutRepairHintData(evidence.CitationRoots{}, draft, resolved, ev, nil)["repair_targets"].([]map[string]any)
			if len(targets) != 1 || targets[0]["use"] != tc.use || (targets[0]["worker_output"] == true) != tc.worker {
				t.Fatalf("targets = %+v", targets)
			}
			retained := RetainedCloseout{Active: true}
			retained.Retain(draft)
			opts := DraftParseOptions(evidence.CitationRoots{}, tc.ev)
			read := ReadCloseoutReport("[cite: c1 => "+tc.use+"]", retained, false, opts)
			after := ResolveDraftCitations(evidence.CitationRoots{}, "implement_investigate", read.Draft, ev, nil)
			if len(read.RepairDefects) != 0 || len(after.Rejected()) != 0 {
				t.Fatalf("the offered repair failed: %+v %+v", read.RepairDefects, after.Rejected())
			}
		})
	}
}

func TestHostClosingStatesStateAndClaimsNothing(t *testing.T) {
	t.Parallel()
	ev := evidence.AssembleLedger(catNumbered(1, "a.go", 1))
	got := HostClosingMarkdown("the limit was reached", ev, "- [ ] audit a.go")
	for _, want := range []string{"ended without a final answer", "the limit was reached", "Nothing below is a finding", "a.go", "- [ ] audit a.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("closing lacks %q: %s", want, got)
		}
	}
	if none := HostClosingMarkdown("x", evidence.InitLedger(), ""); !strings.Contains(none, "No file was read") || strings.Contains(none, "Checklist") {
		t.Fatalf("empty closing = %q", none)
	}
}

// A basename was the most common live miss. It binds only to a single active record.
func TestUniqueFinalSegmentsBindAndTwoNeverDo(t *testing.T) {
	t.Parallel()
	const full = "cmd/gateway/main.go"
	numbered := evidence.AssembleLedger(catNumbered(1, full, 24, 25, 26, 30))
	plain := evidence.AssembleLedger([]evidence.Record{catPlain("read#1", full, 31)})
	two := evidence.AssembleLedger(append(catNumbered(1, full, 30), catNumbered(2, "tools/main.go", 30)...))
	stale := catNumbered(2, "old/main.go", 30)
	stale[0].SupersededBy = "edit#1"
	oneActive := evidence.AssembleLedger(append(catNumbered(1, full, 30), stale...))
	for _, tc := range []struct {
		name, source, present string
		ev                    evidence.Ledger
		want                  CitationOutcome
	}{
		{"live forced-2 shape", "[main.go:30]", "[" + full + ":30](" + full + "#L30)", numbered, CitationGrounded},
		{"a range across records", "[main.go:24-26]", "[" + full + ":24-26](" + full + "#L24-L26)", numbered, CitationGrounded},
		{"more final segments", "[gateway/main.go:30]", "[" + full + ":30](" + full + "#L30)", numbered, CitationGrounded},
		{"a bare basename", "[main.go]", "[" + full + "](" + full + ")", plain, CitationGrounded},
		{"inline code", "`main.go:30`", "[" + full + ":30](" + full + "#L30)", numbered, CitationGrounded},
		{"a superseded record is no candidate", "[main.go:30]", "[" + full + ":30](" + full + "#L30)", oneActive, CitationGrounded},
		{"the coordinate rule still applies", "[main.go:30]", "", plain, CitationUnverifiable},
		{"a gap is still refused", "[main.go:26-30]", "", numbered, CitationUnverifiable},
		{"AC two candidates never bind", "[main.go:30]", "", two, CitationUnverifiable},
		{"two candidates, no line", "[main.go]", "", two, CitationUnverifiable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := CloseoutEvidence{Ledger: tc.ev}
			draft, resolved := repairDraft(t, tc.source, ev)
			if len(resolved.Occurrences) != 1 || resolved.Occurrences[0].Outcome != tc.want {
				t.Fatalf("resolved = %+v, want %s", resolved, tc.want)
			}
			if got := PresentCloseoutMarkdown(draft, resolved); got != tc.present {
				t.Fatalf("presented %q, want %q", got, tc.present)
			}
		})
	}
	// Ambiguity keeps the span code, and the repair names every candidate.
	ev := CloseoutEvidence{Ledger: two}
	code, resolved := repairDraft(t, "`main.go:30`", ev)
	if code.CitationCount() != 0 || PresentCloseoutMarkdown(code, resolved) != "`main.go:30`" {
		t.Fatalf("ambiguous code span = %+v", code.Occurrences)
	}
	draft, rejected := repairDraft(t, "[main.go:30]", ev)
	targets := CloseoutRepairHintData(evidence.CitationRoots{}, draft, rejected, ev, nil)["repair_targets"].([]map[string]any)
	if len(targets) != 1 || targets[0]["candidates"] != "`"+full+":30`, `tools/main.go:30`" {
		t.Fatalf("targets = %+v", targets)
	}
	// A bare word is prose, even when a path ends in it.
	listed := evidence.AssembleLedger(catNumbered(1, "internal/client", 1))
	if prose, _ := repairDraft(t, "The [client] holds.", CloseoutEvidence{Ledger: listed}); prose.CitationCount() != 0 {
		t.Fatalf("a bare word bound: %+v", prose.Occurrences)
	}
}

// The live forced-4 read was `sed -n '1,40p' file | cat -n`, which left no record: sed
// maps no source and cat on a pipe has no pin. The exemption read the ledger correctly.
func TestCitableEvidenceIsWhatTheLedgerHolds(t *testing.T) {
	t.Parallel()
	const ref = "output:program:p1:stdout"
	program := &api.CommandProgramReceipt{Outputs: []api.CommandProgramOutputWindow{{Stream: "stdout", Ref: ref}}}
	row := []api.Message{{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{CommandProgram: program}}}
	delivered := catPlain("read#1", "a.go", 2)
	delivered.SourceDelivery.DeliveryID = ref + ":0:10"
	for _, tc := range []struct {
		name string
		ev   evidence.Ledger
		want bool
	}{
		{"sed through a pipe, echo or git status: no delivered source", evidence.InitLedger(), false},
		{"a window that delivered source", evidence.AssembleLedger([]evidence.Record{delivered}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := CloseoutLedgerHasCitableEvidence(CloseoutEvidence{Ledger: tc.ev}); got != tc.want {
				t.Fatalf("ledger citable = %t", got)
			}
			if got := IntentWindowHasCitableObservations(row, tc.ev); got != tc.want {
				t.Fatalf("window citable = %t", got)
			}
		})
	}
}
