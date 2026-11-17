package actioncard

import (
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestChangedCountsEditedAndNewFilesAndTheirLines(t *testing.T) {
	result := api.ToolResult{Tool: "promote_overlay", FileEditsOmitted: 2,
		FileEditPreviews:  []api.FileEditPreview{{Path: "a.go", Added: 3, Removed: 1}, {Path: "b.go", Added: 10, Created: true}},
		PromotionPreviews: []api.FileEditPreview{{Path: "c.go", Removed: 4, Deleted: true}}}
	changed, ok := fact(ForResult(result, nil).Facts, api.ActionFactChanged)
	if !ok || changed.Files != 4 || changed.Created != 1 || changed.Added != 13 || changed.Removed != 5 {
		t.Fatalf("changed %+v", changed)
	}
}

func TestOneChangedFileOpensIt(t *testing.T) {
	one := api.ToolResult{Tool: "restore_version", FileEditPreviews: []api.FileEditPreview{{Path: "src/a.go", Added: 1}}}
	if d := ForResult(one, nil); d.Opener == nil || d.Opener.Kind != "file" || d.Opener.Path != "src/a.go" {
		t.Fatalf("opener %+v", d.Opener)
	}
	gone := api.ToolResult{Tool: "restore_version", FileEditPreviews: []api.FileEditPreview{{Path: "src/a.go", Deleted: true}}}
	two := api.ToolResult{Tool: "restore_version", FileEditPreviews: []api.FileEditPreview{{Path: "a"}, {Path: "b"}}}
	if ForResult(gone, nil).Opener != nil || ForResult(two, nil).Opener != nil {
		t.Fatal("a deleted file or several files opened one")
	}
}

func TestNetworkNamesHostsOnceAndHowTrafficLeft(t *testing.T) {
	access := &api.ExternalAccess{Modes: []api.ExternalAccessMode{api.ExternalAccessModeMediatedHTTP, api.ExternalAccessModeDirectIP},
		Endpoints: []api.ExternalAccessEndpoint{{Host: "a.test"}, {Host: "a.test"}, {Host: "b.test"}, {Host: "c.test"}, {Host: "d.test"}}}
	network, _ := fact(ForResult(api.ToolResult{Tool: "fetch_url", ExternalAccess: access}, nil).Facts, api.ActionFactNetwork)
	if !reflect.DeepEqual(network.Hosts, []string{"a.test", "b.test", "c.test"}) || network.Egress != "direct" {
		t.Fatalf("network %+v", network)
	}
	unobserved, ok := fact(ForResult(api.ToolResult{Tool: "command", ExternalAccess: &api.ExternalAccess{VisibilitySummary: api.ExternalAccessVisibilitySummaryUnobserved}}, nil).Facts, api.ActionFactNetwork)
	if !ok || unobserved.Egress != "direct" {
		t.Fatalf("unobserved network %+v (ok=%v)", unobserved, ok)
	}
	sockets, ok := fact(ForResult(api.ToolResult{Tool: "command", ExternalAccess: &api.ExternalAccess{Sockets: []api.ExternalAccessSocket{{ApprovedPath: "/tmp/s.sock"}}}}, nil).Facts, api.ActionFactNetwork)
	if !ok || sockets.Egress != "local_only" {
		t.Fatalf("socket network %+v (ok=%v)", sockets, ok)
	}
	bypass, ok := fact(ForResult(api.ToolResult{Tool: "command", ExternalAccess: &api.ExternalAccess{Modes: []api.ExternalAccessMode{api.ExternalAccessModeFullBypass}}}, nil).Facts, api.ActionFactNetwork)
	if !ok || bypass.Egress != "direct" {
		t.Fatalf("full bypass network %+v (ok=%v)", bypass, ok)
	}
}

func TestListeningProcessStatesItsPortInsteadOfCompletion(t *testing.T) {
	d := ForResult(api.ToolResult{Tool: "process_list", Process: &api.ToolProcessHandle{Handle: "job", Running: true}, Listener: &api.LoopbackListenerFact{Address: "[::1]:5173"}}, nil)
	process, _ := fact(d.Facts, api.ActionFactProcess)
	if process.State != "running" || process.Port == nil || *process.Port != 5173 {
		t.Fatalf("process %+v", process)
	}
	if _, ok := fact(d.Facts, api.ActionFactResult); ok || !reflect.DeepEqual(d.Row, []api.ActionFactKind{api.ActionFactProcess}) {
		t.Fatalf("a running process also claimed a result: %+v", d)
	}
}

func TestRefusalBeatsARunningProcess(t *testing.T) {
	result := api.ToolResult{Tool: "terminal_send", ToolArgs: map[string]any{"id": "t"}, Outcome: api.ToolResultOutcomeRejected,
		Codes: []string{"TERMINAL_CLOSED"}, Process: &api.ToolProcessHandle{Handle: "t", Running: true}}
	d := ForResult(result, nil)
	if got, _ := fact(d.Facts, api.ActionFactResult); got.Outcome != "refused" {
		t.Fatalf("result %+v", d.Facts)
	}
	if stopped, _ := fact(d.Facts, api.ActionFactStoppedBy); stopped.Code != "TERMINAL_CLOSED" {
		t.Fatalf("stopped by %+v", stopped)
	}
}

func TestFailureCodeComesFromTheInvocationWhenTheResultHasNone(t *testing.T) {
	result := api.ToolResult{Tool: "fetch_url", Outcome: api.ToolResultOutcomeError,
		Invocation: &api.InvocationReceipt{Status: api.InvocationStatusError, StartedAt: time.Unix(1, 0), Failure: &api.InvocationFailure{Code: "FETCH_TIMEOUT"}}}
	if stopped, _ := fact(ForResult(result, nil).Facts, api.ActionFactStoppedBy); stopped.Code != "FETCH_TIMEOUT" {
		t.Fatalf("stopped by %+v", stopped)
	}
}

func TestVerdictPassesOnlyWhenTerminal(t *testing.T) {
	for terminal, want := range map[bool]string{true: "pass", false: "fail"} {
		d := ForResult(api.ToolResult{Tool: "submit_verdict", Verdict: &api.VerdictOutcome{Verdict: "approve", Terminal: terminal}}, nil)
		if got, _ := fact(d.Facts, api.ActionFactResult); got.Outcome != want {
			t.Fatalf("terminal=%v result %+v", terminal, got)
		}
	}
}

func TestTypedFactsNameTheirSource(t *testing.T) {
	result := api.ToolResult{Tool: "worker_cancel", Dispatch: &api.WorkerDispatch{WorkerID: "w", AgentType: "reviewer"}}
	facts := ForResult(result, nil).Facts
	if worker, _ := fact(facts, api.ActionFactWorker); worker.Role != "reviewer" {
		t.Fatalf("worker %+v", facts)
	}
	approved := api.ToolResult{Tool: "capture_page", Visual: &api.VisualArtifact{ID: "art", Caption: "Home page"},
		CheckpointDecision: &api.CheckpointDecisionMeta{Status: api.CheckpointStatusApproved}}
	facts = ForResult(approved, nil).Facts
	if stopped, _ := fact(facts, api.ActionFactStoppedBy); stopped.By != "approval" || stopped.DecidedBy != "person" {
		t.Fatalf("approval %+v", facts)
	}
	if captured, _ := fact(facts, api.ActionFactCaptured); captured.ArtifactID != "art" || captured.Caption != "Home page" {
		t.Fatalf("captured %+v", facts)
	}
}

func TestStatedFactsJoinTheCardAndFamilyFactsWin(t *testing.T) {
	status := 404
	fetched := api.ToolResult{Tool: "fetch_url", CardFacts: []api.ActionFact{{Kind: api.ActionFactResult, Outcome: "completed", HTTPStatus: &status}}}
	d := ForResult(fetched, nil)
	if got, _ := fact(d.Facts, api.ActionFactResult); got.HTTPStatus == nil || *got.HTTPStatus != 404 {
		t.Fatalf("stated result %+v", d.Facts)
	}
	loaded := api.ToolResult{Tool: "request_tools", CardFacts: []api.ActionFact{
		{Kind: api.ActionFactFound, Count: 2, Noun: "tools", Items: []string{"Fetch page", "Search web"}},
		{Kind: api.ActionFactLoadedBy, By: "engine"},
	}}
	d = ForResult(loaded, nil)
	if !reflect.DeepEqual(kinds(d.Facts), []api.ActionFactKind{api.ActionFactResult, api.ActionFactFound, api.ActionFactLoadedBy}) || !reflect.DeepEqual(d.Row, []api.ActionFactKind{api.ActionFactResult, api.ActionFactFound}) {
		t.Fatalf("loaded tools card %+v", d)
	}
	session := api.ToolResult{Tool: "terminal_close", ToolArgs: map[string]any{"id": "t"}, CardFacts: []api.ActionFact{{Kind: api.ActionFactResult, Outcome: "failed"}}}
	if got, _ := fact(ForResult(session, nil).Facts, api.ActionFactResult); got.Outcome != "completed" {
		t.Fatalf("a stated fact replaced the family's: %+v", got)
	}
}

func TestRowNamesOnlyFactsTheCardKeeps(t *testing.T) {
	result := api.ToolResult{Tool: "terminal_send", ToolArgs: map[string]any{"id": "t"}, Outcome: api.ToolResultOutcomeRejected, Codes: []string{"X"},
		FileEditPreviews: []api.FileEditPreview{{Path: "a", Added: 1}},
		CardFacts:        []api.ActionFact{{Kind: api.ActionFactFound, Count: 1, Noun: "results"}},
		Invocation:       &api.InvocationReceipt{StartedAt: time.Unix(1, 0), SettledAt: &[]time.Time{time.Unix(3, 0)}[0]}}
	d := ForResult(result, nil)
	if _, ok := fact(d.Facts, api.ActionFactSteps); ok {
		t.Fatalf("steps survived a full card: %v", kinds(d.Facts))
	}
	for _, kind := range d.Row {
		if _, ok := fact(d.Facts, kind); !ok {
			t.Fatalf("row %v names %q, which the card dropped", d.Row, kind)
		}
	}
}

func TestPathArgumentOpensInFiles(t *testing.T) {
	summary := api.ToolResult{Tool: "summarize", ToolArgs: map[string]any{"path": " @other/src ", "task": "purpose"}}
	if d := ForResult(summary, nil); d.Opener == nil || d.Opener.Kind != "file" || d.Opener.Path != "@other/src" {
		t.Fatalf("summary opener %+v", d.Opener)
	}
	activity := api.CommandActivity{ID: "a", State: "completed"}
	survey := api.ToolResult{Tool: "survey_repo", ToolArgs: map[string]any{"path": "."}}
	if d := ForActivity(api.CommandProgramReceipt{ID: "p", Activities: []api.CommandActivity{activity}}, activity, survey, nil); d.Opener == nil || d.Opener.Path != "." {
		t.Fatalf("survey activity opener %+v", d.Opener)
	}
	edited := api.ToolResult{Tool: "restore_version", ToolArgs: map[string]any{"path": "old.go"}, FileEditPreviews: []api.FileEditPreview{{Path: "new.go", Added: 1}}}
	if d := ForResult(edited, nil); d.Opener.Path != "new.go" {
		t.Fatalf("the changed file should win over the argument: %+v", d.Opener)
	}
	if d := ForResult(api.ToolResult{Tool: "web_search", ToolArgs: map[string]any{"query": "x"}}, nil); d.Opener != nil {
		t.Fatalf("a call naming no path opened one: %+v", d.Opener)
	}
}
